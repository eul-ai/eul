package filesearch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	maxResults       = 100
	maxCatalogs      = 16
	maxCachedEntries = 500_000
)

type Request struct {
	ID      uint64
	Query   string
	Refresh bool
}

type State uint8

const (
	StateDiscovering State = iota
	StateComplete
	StateLimited
	StateFailed
)

type Result struct {
	ID      uint64
	Matches []Match
	State   State
	Err     error
}

type searchCommand struct {
	request *Request
	cancel  bool
}

type resolveFileSearchFunc func(string, string, string, string) (fileSearchSpec, error)

type cacheLimits struct {
	maxCatalogs int
	maxEntries  int
}

type Searcher struct {
	cwd          string
	canonicalCWD string
	home         string
	resolve      resolveFileSearchFunc
	discover     discoverFilesFunc
	cacheLimits  cacheLimits
	commands     chan searcherUpdate
	discoveries  chan fileDiscoveryUpdate
	matches      chan fileMatchUpdate
	results      chan Result
	stop         chan struct{}
	done         chan struct{}
	wait         sync.WaitGroup
	closeOnce    sync.Once
}

type searcherUpdate struct {
	ctx     context.Context
	command searchCommand
}

type fileDiscoveryUpdate struct {
	key        fileSearchKey
	generation uint64
	batch      []fileCandidate
	done       bool
	limited    bool
	err        error
}

type fileMatchUpdate struct {
	generation uint64
	requestID  uint64
	matches    []Match
	state      State
	err        error
}

type fileSearchCatalog struct {
	entries        map[string]fileCandidate
	visible        map[string]fileCandidate
	refresh        map[string]fileCandidate
	state          State
	committedState State
	err            error
	initialized    bool
	generation     uint64
	lastUsed       uint64
	cancel         context.CancelFunc
}

type activeFileSearch struct {
	ctx     context.Context
	request Request
	spec    fileSearchSpec
	key     fileSearchKey
}

func New(cwd string) *Searcher {
	home, _ := os.UserHomeDir()
	return newConfiguredSearcher(cwd, home, resolveFileSearchSpec, discoverFiles)
}

func newConfiguredSearcher(cwd, home string, resolve resolveFileSearchFunc, discover discoverFilesFunc) *Searcher {
	return newConfiguredSearcherWithLimits(cwd, home, resolve, discover, cacheLimits{
		maxCatalogs: maxCatalogs,
		maxEntries:  maxCachedEntries,
	})
}

func newConfiguredSearcherWithLimits(cwd, home string, resolve resolveFileSearchFunc, discover discoverFilesFunc, limits cacheLimits) *Searcher {
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		canonicalCWD = filepath.Clean(cwd)
	}
	if home != "" {
		home = filepath.Clean(home)
	}
	searcher := &Searcher{
		cwd:          filepath.Clean(cwd),
		canonicalCWD: filepath.Clean(canonicalCWD),
		home:         home,
		resolve:      resolve,
		discover:     discover,
		cacheLimits:  limits,
		commands:     make(chan searcherUpdate, 1),
		discoveries:  make(chan fileDiscoveryUpdate, 64),
		matches:      make(chan fileMatchUpdate, 64),
		results:      make(chan Result, 64),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go searcher.run()
	return searcher
}

func (s *Searcher) Search(ctx context.Context, request Request) {
	s.update(ctx, searchCommand{request: &request})
}

func (s *Searcher) Cancel() {
	s.update(context.Background(), searchCommand{cancel: true})
}

func (s *Searcher) Results() <-chan Result {
	return s.results
}

func (s *Searcher) update(ctx context.Context, command searchCommand) {
	update := searcherUpdate{ctx: ctx, command: command}
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.stop:
			return
		default:
		}

		select {
		case s.commands <- update:
			return
		default:
		}

		select {
		case <-s.commands:
		default:
		}
	}
}

func (s *Searcher) Close() {
	s.closeOnce.Do(func() {
		close(s.stop)
		<-s.done
		s.wait.Wait()
	})
}

func (s *Searcher) run() {
	defer close(s.done)
	defer close(s.results)

	catalogs := make(map[fileSearchKey]*fileSearchCatalog)
	var active *activeFileSearch
	var matchCancel context.CancelFunc
	var matchGeneration uint64
	var discoveryGeneration uint64
	var catalogClock uint64
	var catalogEntries int
	var pendingOutput chan<- Result
	var pendingResult Result
	var pendingDone <-chan struct{}

	clearPending := func() {
		pendingOutput = nil
		pendingDone = nil
	}
	publish := func(ctx context.Context, result Result) {
		if ctx.Err() != nil {
			return
		}
		pendingOutput = s.results
		pendingResult = result
		pendingDone = ctx.Done()
	}

	cancelMatch := func() {
		if matchCancel != nil {
			matchCancel()
			matchCancel = nil
		}
	}
	cancelCatalog := func(catalog *fileSearchCatalog) {
		if catalog == nil || catalog.cancel == nil {
			return
		}
		catalog.cancel()
		catalog.cancel = nil
		discoveryGeneration++
		catalog.generation = discoveryGeneration
		catalog.refresh = nil
		catalog.visible = catalog.entries
		catalog.err = nil
		if catalog.initialized {
			catalog.state = catalog.committedState
		} else {
			catalog.state = StateDiscovering
		}
	}
	trimCatalogs := func() {
		for len(catalogs) > s.cacheLimits.maxCatalogs || catalogEntries > s.cacheLimits.maxEntries {
			var oldestKey fileSearchKey
			var oldest *fileSearchCatalog
			for key, catalog := range catalogs {
				if active != nil && key == active.key {
					continue
				}
				if oldest != nil && catalog.lastUsed >= oldest.lastUsed {
					continue
				}
				oldestKey = key
				oldest = catalog
			}
			if oldest == nil {
				return
			}
			cancelCatalog(oldest)
			catalogEntries -= len(oldest.entries)
			delete(catalogs, oldestKey)
		}
	}
	cancelActive := func() {
		cancelMatch()
		if active != nil {
			cancelCatalog(catalogs[active.key])
		}
		active = nil
		trimCatalogs()
	}
	startMatch := func() {
		cancelMatch()
		if active == nil {
			return
		}
		catalog := catalogs[active.key]
		candidates := fileCandidateValues(catalog.visible)
		state := catalog.state
		errText := catalog.err
		requestID := active.request.ID
		spec := active.spec
		matchGeneration++
		generation := matchGeneration
		matchContext, cancel := context.WithCancel(active.ctx)
		matchCancel = cancel

		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			defer cancel()

			matches := rankFileCandidates(matchContext, s.canonicalCWD, spec, candidates)
			if matchContext.Err() != nil {
				return
			}
			update := fileMatchUpdate{
				generation: generation,
				requestID:  requestID,
				matches:    matches,
				state:      state,
				err:        errText,
			}
			select {
			case s.matches <- update:
			case <-matchContext.Done():
			case <-s.stop:
			}
		}()
	}
	startDiscovery := func(ctx context.Context, key fileSearchKey, spec fileSearchSpec, catalog *fileSearchCatalog) {
		cancelCatalog(catalog)
		discoveryGeneration++
		catalog.generation = discoveryGeneration
		generation := catalog.generation
		catalog.refresh = make(map[string]fileCandidate)
		catalog.visible = cloneFileCandidates(catalog.entries)
		catalog.state = StateDiscovering
		catalog.err = nil
		discoveryContext, cancel := context.WithCancel(ctx)
		catalog.cancel = cancel

		s.wait.Add(1)
		go func() {
			defer s.wait.Done()
			defer cancel()

			emit := func(batch []fileCandidate) error {
				update := fileDiscoveryUpdate{key: key, generation: generation, batch: batch}
				select {
				case s.discoveries <- update:
					return nil
				case <-discoveryContext.Done():
					return discoveryContext.Err()
				case <-s.stop:
					return context.Canceled
				}
			}
			limited, err := s.discover(discoveryContext, spec, emit)
			update := fileDiscoveryUpdate{key: key, generation: generation, done: true, limited: limited, err: err}
			select {
			case s.discoveries <- update:
			case <-s.stop:
			}
		}()
	}
	applyDiscoveryUpdate := func(update fileDiscoveryUpdate) bool {
		catalog := catalogs[update.key]
		if catalog == nil || catalog.generation != update.generation {
			return false
		}
		for _, candidate := range update.batch {
			catalog.refresh[candidate.path] = candidate
			catalog.visible[candidate.path] = candidate
		}

		if update.done {
			catalog.cancel = nil
			previousEntries := len(catalog.entries)
			switch {
			case update.err != nil && !errors.Is(update.err, context.Canceled):
				catalog.refresh = nil
				catalog.visible = catalog.entries
				catalog.state = StateFailed
				catalog.err = update.err
			case update.err != nil:
				catalog.refresh = nil
				catalog.visible = catalog.entries
				catalog.err = nil
				if catalog.initialized {
					catalog.state = catalog.committedState
				} else {
					catalog.state = StateDiscovering
				}
			case update.limited:
				catalog.entries = catalog.refresh
				catalog.visible = catalog.entries
				catalog.refresh = nil
				catalog.initialized = true
				catalog.state = StateLimited
				catalog.committedState = StateLimited
			default:
				catalog.entries = catalog.refresh
				catalog.visible = catalog.entries
				catalog.refresh = nil
				catalog.initialized = true
				catalog.state = StateComplete
				catalog.committedState = StateComplete
				catalog.err = nil
			}
			if update.err == nil {
				catalogEntries += len(catalog.entries) - previousEntries
				trimCatalogs()
			}
		}
		return active != nil && active.key == update.key
	}

	for {
		select {
		case update := <-s.commands:
			clearPending()
			command := update.command
			if command.cancel {
				cancelActive()
			}
			if command.request == nil {
				continue
			}

			var spec fileSearchSpec
			var err error
			resolved := false
			if active != nil {
				spec, resolved = rescoreFileSearchSpec(active.spec, command.request.Query)
			}
			if !resolved {
				spec, err = s.resolve(s.cwd, s.canonicalCWD, s.home, command.request.Query)
			}
			if err != nil {
				cancelActive()
				publish(update.ctx, Result{ID: command.request.ID, State: StateFailed, Err: err})
				continue
			}

			key := spec.key()
			if active != nil && active.key != key {
				cancelCatalog(catalogs[active.key])
			}
			cancelMatch()
			active = &activeFileSearch{
				ctx:     update.ctx,
				request: *command.request,
				spec:    spec,
				key:     key,
			}

			catalog := catalogs[key]
			if catalog == nil {
				catalog = &fileSearchCatalog{
					entries:        make(map[string]fileCandidate),
					visible:        make(map[string]fileCandidate),
					state:          StateDiscovering,
					committedState: StateComplete,
				}
				catalogs[key] = catalog
			}
			catalogClock++
			catalog.lastUsed = catalogClock
			trimCatalogs()
			if command.request.Refresh || !catalog.initialized && catalog.cancel == nil {
				startDiscovery(update.ctx, key, spec, catalog)
			}
			startMatch()

		case update := <-s.discoveries:
			rematch := applyDiscoveryUpdate(update)
			for range len(s.discoveries) {
				if applyDiscoveryUpdate(<-s.discoveries) {
					rematch = true
				}
			}
			if rematch {
				startMatch()
			}

		case update := <-s.matches:
			if active == nil || update.generation != matchGeneration || update.requestID != active.request.ID {
				continue
			}
			publish(active.ctx, Result{
				ID:      update.requestID,
				Matches: update.matches,
				State:   update.state,
				Err:     update.err,
			})

		case pendingOutput <- pendingResult:
			clearPending()

		case <-pendingDone:
			clearPending()

		case <-s.stop:
			cancelActive()
			for _, catalog := range catalogs {
				cancelCatalog(catalog)
			}
			return
		}
	}
}

func cloneFileCandidates(source map[string]fileCandidate) map[string]fileCandidate {
	cloned := make(map[string]fileCandidate, len(source))
	for path, candidate := range source {
		cloned[path] = candidate
	}
	return cloned
}

func fileCandidateValues(source map[string]fileCandidate) []fileCandidate {
	values := make([]fileCandidate, 0, len(source))
	for _, candidate := range source {
		values = append(values, candidate)
	}
	return values
}
