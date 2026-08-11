package session

import (
	"errors"
	"fmt"

	"github.com/eul-ai/eul/tool"
	lsptool "github.com/eul-ai/eul/tool/lsp"
)

type toolAccess uint8

const (
	fullToolAccess toolAccess = iota
	readOnlyToolAccess
)

type toolsetFactory func(string, toolAccess, tool.NetworkAuthorizer, ...tool.Tool) (*tool.Registry, error)

func finishRegistry(runErr error, registry *tool.Registry, operation string) error {
	closeErr := registry.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("%s: %w", operation, closeErr)
	}
	return errors.Join(runErr, closeErr)
}

func buildToolset(cwd string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	return buildToolsetWithHomeAndNetworkAuthorizer(cwd, "", access, nil, additional...)
}

func buildToolsetWithHome(cwd, home string, access toolAccess, additional ...tool.Tool) (*tool.Registry, error) {
	return buildToolsetWithHomeAndNetworkAuthorizer(cwd, home, access, nil, additional...)
}

func buildToolsetWithHomeAndNetworkAuthorizer(
	cwd string,
	home string,
	access toolAccess,
	authorizeNetwork tool.NetworkAuthorizer,
	additional ...tool.Tool,
) (*tool.Registry, error) {
	var tools []tool.Tool
	var lsp *lsptool.Set
	var err error
	switch access {
	case fullToolAccess:
		tools = []tool.Tool{
			tool.NewRead(cwd),
			tool.NewWrite(cwd),
			tool.NewEdit(cwd),
			tool.NewBashWithNetworkAuthorizer(cwd, authorizeNetwork),
		}
		lsp, err = lsptool.New(cwd, home)
	case readOnlyToolAccess:
		tools = []tool.Tool{tool.NewRead(cwd)}
		lsp, err = lsptool.NewReadOnly(cwd, home)
	default:
		return nil, errors.New("unknown tool access")
	}
	if err != nil {
		return nil, fmt.Errorf("configure LSP: %w", err)
	}

	tools = append(tools, lsp.Tools()...)
	tools = append(tools, additional...)
	registry, err := tool.NewRegistry(tools, lsp)
	if err != nil {
		_ = lsp.Close()
		return nil, err
	}
	return registry, nil
}
