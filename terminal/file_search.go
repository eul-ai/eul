package terminal

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
)

const (
	filePickerMaxResults  = 100
	fileSearchMaxCatalogs = 16
)

type fileSearchRequest struct {
	id      uint64
	query   string
	refresh bool
}

type fileSearchState uint8

const (
	fileSearchDiscovering fileSearchState = iota
	fileSearchComplete
	fileSearchLimited
	fileSearchFailed
)

type fileSearchResult struct {
	id      uint64
	matches []fileSearchMatch
	state   fileSearchState
	err     string
}

type fileSearchCommand struct {
	request *fileSearchRequest
	cancel  bool
}

type resolveFileSearchFunc func(string, string, string, string) (fileSearchSpec, error)

type fileSearchRunner struct {
	cwd          string
	canonicalCWD string
	home         string
	resolve      resolveFileSearchFunc
	discover     discoverFilesFunc
	commands     chan fileSearchRunnerUpdate
	discoveries  chan fileDiscoveryUpdate
	matches      chan fileMatchUpdate
	stop         chan struct{}
	done         chan struct{}
	wait         sync.WaitGroup
	closeOnce    sync.Once
}

type fileSearchRunnerUpdate struct {
	ctx     context.Context
	command fileSearchCommand
	output  chan<- fileSearchResult
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
	matches    []fileSearchMatch
	state      fileSearchState
	err        string
}

type fileSearchCatalog struct {
	entries        map[string]fileCandidate
	visible        map[string]fileCandidate
	refresh        map[string]fileCandidate
	state          fileSearchState
	committedState fileSearchState
	err            string
	initialized    bool
	generation     uint64
	lastUsed       uint64
	cancel         context.CancelFunc
}

type activeFileSearch struct {
	ctx     context.Context
	request fileSearchRequest
	spec    fileSearchSpec
	key     fileSearchKey
	output  chan<- fileSearchResult
}

func newFileSearchRunner(cwd string) *fileSearchRunner {
	home, _ := os.UserHomeDir()
	return newConfiguredFileSearchRunner(cwd, home, resolveFileSearchSpec, discoverFiles)
}

func newConfiguredFileSearchRunner(cwd, home string, resolve resolveFileSearchFunc, discover discoverFilesFunc) *fileSearchRunner {
	canonicalCWD, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		canonicalCWD = filepath.Clean(cwd)
	}
	if home != "" {
		home = filepath.Clean(home)
	}
	runner := &fileSearchRunner{
		cwd:          filepath.Clean(cwd),
		canonicalCWD: filepath.Clean(canonicalCWD),
		home:         home,
		resolve:      resolve,
		discover:     discover,
		commands:     make(chan fileSearchRunnerUpdate, 1),
		discoveries:  make(chan fileDiscoveryUpdate, 64),
		matches:      make(chan fileMatchUpdate, 64),
		stop:         make(chan struct{}),
		done:         make(chan struct{}),
	}
	go runner.run()
	return runner
}

func (r *fileSearchRunner) update(ctx context.Context, command fileSearchCommand, output chan<- fileSearchResult) {
	if command.request == nil && !command.cancel {
		return
	}
	update := fileSearchRunnerUpdate{ctx: ctx, command: command, output: output}
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.stop:
			return
		default:
		}

		select {
		case r.commands <- update:
			return
		default:
		}

		select {
		case <-r.commands:
		default:
		}
	}
}

func (r *fileSearchRunner) close() {
	r.closeOnce.Do(func() {
		close(r.stop)
		<-r.done
		r.wait.Wait()
	})
}

func (r *fileSearchRunner) run() {
	defer close(r.done)

	catalogs := make(map[fileSearchKey]*fileSearchCatalog)
	var active *activeFileSearch
	var matchCancel context.CancelFunc
	var matchGeneration uint64
	var discoveryGeneration uint64
	var catalogClock uint64
	var pendingOutput chan<- fileSearchResult
	var pendingResult fileSearchResult
	var pendingDone <-chan struct{}

	clearPending := func() {
		pendingOutput = nil
		pendingDone = nil
	}
	publish := func(ctx context.Context, output chan<- fileSearchResult, result fileSearchResult) {
		if ctx.Err() != nil {
			return
		}
		pendingOutput = output
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
		catalog.visible = cloneFileCandidates(catalog.entries)
		catalog.err = ""
		if catalog.initialized {
			catalog.state = catalog.committedState
		} else {
			catalog.state = fileSearchDiscovering
		}
	}
	evictCatalog := func(except fileSearchKey) {
		if len(catalogs) < fileSearchMaxCatalogs {
			return
		}
		var oldestKey fileSearchKey
		var oldest *fileSearchCatalog
		for key, catalog := range catalogs {
			if key == except || oldest != nil && catalog.lastUsed >= oldest.lastUsed {
				continue
			}
			oldestKey = key
			oldest = catalog
		}
		if oldest == nil {
			return
		}
		cancelCatalog(oldest)
		delete(catalogs, oldestKey)
	}
	cancelActive := func() {
		cancelMatch()
		if active != nil {
			cancelCatalog(catalogs[active.key])
		}
		active = nil
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
		requestID := active.request.id
		spec := active.spec
		matchGeneration++
		generation := matchGeneration
		matchContext, cancel := context.WithCancel(active.ctx)
		matchCancel = cancel

		r.wait.Add(1)
		go func() {
			defer r.wait.Done()
			matches := rankFileCandidates(matchContext, r.canonicalCWD, spec, candidates)
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
			case r.matches <- update:
			case <-matchContext.Done():
			case <-r.stop:
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
		catalog.state = fileSearchDiscovering
		catalog.err = ""
		discoveryContext, cancel := context.WithCancel(ctx)
		catalog.cancel = cancel

		r.wait.Add(1)
		go func() {
			defer r.wait.Done()
			emit := func(batch []fileCandidate) error {
				update := fileDiscoveryUpdate{key: key, generation: generation, batch: batch}
				select {
				case r.discoveries <- update:
					return nil
				case <-discoveryContext.Done():
					return discoveryContext.Err()
				case <-r.stop:
					return context.Canceled
				}
			}
			limited, err := r.discover(discoveryContext, spec, emit)
			update := fileDiscoveryUpdate{key: key, generation: generation, done: true, limited: limited, err: err}
			select {
			case r.discoveries <- update:
			case <-r.stop:
			}
		}()
	}

	for {
		select {
		case update := <-r.commands:
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
				spec, resolved = rescoreFileSearchSpec(active.spec, command.request.query)
			}
			if !resolved {
				spec, err = r.resolve(r.cwd, r.canonicalCWD, r.home, command.request.query)
			}
			if err != nil {
				cancelActive()
				publish(update.ctx, update.output, fileSearchResult{id: command.request.id, state: fileSearchFailed, err: err.Error()})
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
				output:  update.output,
			}

			catalog := catalogs[key]
			if catalog == nil {
				evictCatalog(key)
				catalog = &fileSearchCatalog{
					entries:        make(map[string]fileCandidate),
					visible:        make(map[string]fileCandidate),
					state:          fileSearchDiscovering,
					committedState: fileSearchComplete,
				}
				catalogs[key] = catalog
			}
			catalogClock++
			catalog.lastUsed = catalogClock
			if command.request.refresh || !catalog.initialized && catalog.cancel == nil {
				startDiscovery(update.ctx, key, spec, catalog)
			}
			startMatch()

		case update := <-r.discoveries:
			catalog := catalogs[update.key]
			if catalog == nil || catalog.generation != update.generation {
				continue
			}
			for _, candidate := range update.batch {
				catalog.refresh[candidate.path] = candidate
				catalog.visible[candidate.path] = candidate
			}
			if update.done {
				catalog.cancel = nil
				switch {
				case update.err != nil && !errors.Is(update.err, context.Canceled):
					catalog.refresh = nil
					catalog.visible = cloneFileCandidates(catalog.entries)
					catalog.state = fileSearchFailed
					catalog.err = update.err.Error()
				case update.err != nil:
					catalog.refresh = nil
					catalog.visible = cloneFileCandidates(catalog.entries)
					catalog.err = ""
					if catalog.initialized {
						catalog.state = catalog.committedState
					} else {
						catalog.state = fileSearchDiscovering
					}
				case update.limited:
					catalog.entries = catalog.refresh
					catalog.visible = catalog.entries
					catalog.refresh = nil
					catalog.initialized = true
					catalog.state = fileSearchLimited
					catalog.committedState = fileSearchLimited
				default:
					catalog.entries = catalog.refresh
					catalog.visible = catalog.entries
					catalog.refresh = nil
					catalog.initialized = true
					catalog.state = fileSearchComplete
					catalog.committedState = fileSearchComplete
					catalog.err = ""
				}
			}
			if active != nil && active.key == update.key {
				startMatch()
			}

		case update := <-r.matches:
			if active == nil || update.generation != matchGeneration || update.requestID != active.request.id {
				continue
			}
			publish(active.ctx, active.output, fileSearchResult{
				id:      update.requestID,
				matches: update.matches,
				state:   update.state,
				err:     update.err,
			})

		case pendingOutput <- pendingResult:
			clearPending()

		case <-pendingDone:
			clearPending()

		case <-r.stop:
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
