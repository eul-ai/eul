package interactive

import (
	"errors"
	"fmt"

	"github.com/eul-ai/eul/tool"
)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	readOnlyToolAccess
)

type toolsetFactory func(string, toolAccess, bool, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error)

func finishRegistry(runErr error, registry *tool.Registry, operation string) error {
	closeErr := registry.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("%s: %w", operation, closeErr)
	}
	return errors.Join(runErr, closeErr)
}

func buildToolsetWithHomeAndNetworkAuthorizer(
	cwd string,
	home string,
	access toolAccess,
	noSandbox bool,
	authorizeNetwork tool.NetworkAuthorizer,
	additional ...tool.Tool,
) (*tool.Registry, error) {
	var tools []tool.Tool
	var includeRename bool
	switch access {
	case fullToolAccess:
		bash := tool.NewBashWithNetworkAuthorizer(cwd, authorizeNetwork)
		if noSandbox {
			bash = tool.NewBashWithoutSandbox(cwd)
		}
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewEdit(cwd),
			bash,
		}
		includeRename = true
	case readOnlyToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd)}
	default:
		return nil, errors.New("unknown tool access")
	}
	lspTools, lspService, err := tool.NewLSP(cwd, home, includeRename)
	if err != nil {
		return nil, fmt.Errorf("configure LSP: %w", err)
	}

	tools = append(tools, lspTools...)
	tools = append(tools, additional...)
	registry, err := tool.NewRegistry(tools, lspService)
	if err != nil {
		_ = lspService.Close()
		return nil, err
	}
	return registry, nil
}
