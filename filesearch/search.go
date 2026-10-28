package filesearch

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
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

type Searcher struct {
	cwd          string
	canonicalCWD string
	home         string
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
	entries     map[string]fileCandidate
	visible     map[string]fileCandidate
	refresh     map[string]fileCandidate
	state       State
	err         error
	initialized bool
	limited     bool
	generation  uint64
	lastUsed    uint64
	cancel      context.CancelFunc
}

type activeFileSearch struct {
	ctx     context.Context
	request Request
	spec    fileSearchSpec
	key     fileSearchKey
}

func New(cwd string) *Searcher {
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		canonicalCWD = cwd
	}
	home, _ := os.UserHomeDir()
	if home != "" {
		home = filepath.Clean(home)
	}
	searcher := &Searcher{
		cwd:          filepath.Clean(cwd),
		canonicalCWD: filepath.Clean(canonicalCWD),
		home:         home,
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

type searchLoop struct {
	searcher            *Searcher
	catalogs            map[fileSearchKey]*fileSearchCatalog
	active              *activeFileSearch
	matchCancel         context.CancelFunc
	matchGeneration     uint64
	discoveryGeneration uint64
	catalogClock        uint64
	catalogEntries      int
	pendingOutput       chan<- Result
	pendingResult       Result
	pendingDone         <-chan struct{}
}

func (s *Searcher) run() {
	defer close(s.done)
	defer close(s.results)

	loop := &searchLoop{searcher: s, catalogs: make(map[fileSearchKey]*fileSearchCatalog)}
	for {
		select {
		case update := <-s.commands:
			loop.clearPending()
			loop.handleCommand(update)

		case update := <-s.discoveries:
			rematch := loop.applyDiscovery(update)
			for range len(s.discoveries) {
				if loop.applyDiscovery(<-s.discoveries) {
					rematch = true
				}
			}
			if rematch {
				loop.startMatch()
			}

		case update := <-s.matches:
			loop.applyMatch(update)

		case loop.pendingOutput <- loop.pendingResult:
			loop.clearPending()

		case <-loop.pendingDone:
			loop.clearPending()

		case <-s.stop:
			loop.cancelActive()
			for _, catalog := range loop.catalogs {
				loop.cancelCatalog(catalog)
			}
			return
		}
	}
}

func (l *searchLoop) handleCommand(update searcherUpdate) {
	command := update.command
	if command.cancel {
		l.cancelActive()
	}
	if command.request == nil {
		return
	}

	spec, err := l.resolve(command.request.Query)
	if err != nil {
		l.cancelActive()
		l.publish(update.ctx, Result{ID: command.request.ID, State: StateFailed, Err: err})
		return
	}

	key := spec.key()
	if l.active != nil && l.active.key != key {
		l.cancelCatalog(l.catalogs[l.active.key])
	}
	l.cancelMatch()
	l.active = &activeFileSearch{ctx: update.ctx, request: *command.request, spec: spec, key: key}

	catalog := l.catalogs[key]
	if catalog == nil {
		catalog = &fileSearchCatalog{
			entries: make(map[string]fileCandidate),
			visible: make(map[string]fileCandidate),
			state:   StateDiscovering,
		}
		l.catalogs[key] = catalog
	}
	l.catalogClock++
	catalog.lastUsed = l.catalogClock
	l.evict()
	if command.request.Refresh || !catalog.initialized && catalog.cancel == nil {
		l.startDiscovery(update.ctx, key, spec, catalog)
	}
	l.startMatch()
}

func (l *searchLoop) resolve(query string) (fileSearchSpec, error) {
	if l.active != nil {
		if spec, ok := rescoreFileSearchSpec(l.active.spec, query); ok {
			return spec, nil
		}
	}
	return resolveFileSearchSpec(l.searcher.cwd, l.searcher.canonicalCWD, l.searcher.home, query)
}

func (l *searchLoop) startDiscovery(ctx context.Context, key fileSearchKey, spec fileSearchSpec, catalog *fileSearchCatalog) {
	l.cancelCatalog(catalog)
	l.discoveryGeneration++
	catalog.generation = l.discoveryGeneration
	generation := catalog.generation
	catalog.refresh = make(map[string]fileCandidate)
	catalog.visible = maps.Clone(catalog.entries)
	catalog.state = StateDiscovering
	catalog.err = nil
	discoveryContext, cancel := context.WithCancel(ctx)
	catalog.cancel = cancel

	s := l.searcher
	s.wait.Go(func() {
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
		limited, err := discoverFiles(discoveryContext, spec, emit)
		update := fileDiscoveryUpdate{key: key, generation: generation, done: true, limited: limited, err: err}
		select {
		case s.discoveries <- update:
		case <-s.stop:
		}
	})
}

func (l *searchLoop) applyDiscovery(update fileDiscoveryUpdate) bool {
	catalog := l.catalogs[update.key]
	if catalog == nil || catalog.generation != update.generation {
		return false
	}
	for _, candidate := range update.batch {
		catalog.refresh[candidate.path] = candidate
		catalog.visible[candidate.path] = candidate
	}

	if update.done {
		catalog.cancel = nil
		switch {
		case errors.Is(update.err, context.Canceled):
			catalog.showEntries()
		case update.err != nil:
			catalog.showEntries()
			catalog.state = StateFailed
			catalog.err = update.err
		default:
			previousEntries := len(catalog.entries)
			catalog.entries = catalog.refresh
			catalog.initialized = true
			catalog.limited = update.limited
			catalog.showEntries()
			l.catalogEntries += len(catalog.entries) - previousEntries
			l.evict()
		}
	}
	return l.active != nil && l.active.key == update.key
}

func (l *searchLoop) startMatch() {
	l.cancelMatch()
	if l.active == nil {
		return
	}

	catalog := l.catalogs[l.active.key]
	candidates := slices.Collect(maps.Values(catalog.visible))
	state := catalog.state
	catalogErr := catalog.err
	requestID := l.active.request.ID
	spec := l.active.spec
	l.matchGeneration++
	generation := l.matchGeneration
	matchContext, cancel := context.WithCancel(l.active.ctx)
	l.matchCancel = cancel

	s := l.searcher
	s.wait.Go(func() {
		defer cancel()

		matches := rankFileCandidates(matchContext, s.canonicalCWD, spec, candidates)
		if matchContext.Err() != nil {
			return
		}
		update := fileMatchUpdate{generation: generation, requestID: requestID, matches: matches, state: state, err: catalogErr}
		select {
		case s.matches <- update:
		case <-matchContext.Done():
		case <-s.stop:
		}
	})
}

func (l *searchLoop) applyMatch(update fileMatchUpdate) {
	if l.active == nil || update.generation != l.matchGeneration || update.requestID != l.active.request.ID {
		return
	}
	l.publish(l.active.ctx, Result{ID: update.requestID, Matches: update.matches, State: update.state, Err: update.err})
}

func (l *searchLoop) publish(ctx context.Context, result Result) {
	if ctx.Err() != nil {
		return
	}
	l.pendingOutput = l.searcher.results
	l.pendingResult = result
	l.pendingDone = ctx.Done()
}

func (l *searchLoop) clearPending() {
	l.pendingOutput = nil
	l.pendingDone = nil
}

func (l *searchLoop) cancelMatch() {
	if l.matchCancel != nil {
		l.matchCancel()
		l.matchCancel = nil
	}
}

func (l *searchLoop) cancelActive() {
	l.cancelMatch()
	if l.active != nil {
		l.cancelCatalog(l.catalogs[l.active.key])
	}
	l.active = nil
	l.evict()
}

func (l *searchLoop) cancelCatalog(catalog *fileSearchCatalog) {
	if catalog == nil || catalog.cancel == nil {
		return
	}
	catalog.cancel()
	catalog.cancel = nil
	l.discoveryGeneration++
	catalog.generation = l.discoveryGeneration
	catalog.showEntries()
}

func (l *searchLoop) evict() {
	for len(l.catalogs) > maxCatalogs || l.catalogEntries > maxCachedEntries {
		var oldestKey fileSearchKey
		var oldest *fileSearchCatalog
		for key, catalog := range l.catalogs {
			if l.active != nil && key == l.active.key {
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
		l.cancelCatalog(oldest)
		l.catalogEntries -= len(oldest.entries)
		delete(l.catalogs, oldestKey)
	}
}

func (c *fileSearchCatalog) showEntries() {
	c.refresh = nil
	c.visible = c.entries
	c.err = nil
	switch {
	case !c.initialized:
		c.state = StateDiscovering
	case c.limited:
		c.state = StateLimited
	default:
		c.state = StateComplete
	}
}
