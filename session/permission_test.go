package session

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/eul-ai/eul/agent"
)

type authorization struct {
	allowed bool
	err     error
}

func authorizeAsync(ctx context.Context, s *Session, command string) <-chan authorization {
	done := make(chan authorization, 1)
	go func() {
		allowed, err := s.AuthorizeNetwork(ctx, command)
		done <- authorization{allowed: allowed, err: err}
	}()
	return done
}

func awaitPermissions(t *testing.T, s *Session, count int) []*PermissionRequest {
	t.Helper()
	timeout := time.After(2 * time.Second)
	for {
		if requests := s.Permissions(); len(requests) == count {
			return requests
		}
		select {
		case <-s.Updates():
		case <-timeout:
			t.Fatalf("permissions = %d, want %d", len(s.Permissions()), count)
		}
	}
}

func awaitAuthorization(t *testing.T, done <-chan authorization) authorization {
	t.Helper()
	select {
	case result := <-done:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("authorization did not finish")
		return authorization{}
	}
}

func TestAuthorizeNetworkAnswersRequestsInOrder(t *testing.T) {
	conversation := openTestSession(t, createTestHandle(t), nil)
	defer conversation.Close()

	first := authorizeAsync(context.Background(), conversation, "git push")
	awaitPermissions(t, conversation, 1)
	second := authorizeAsync(context.Background(), conversation, "ssh host")
	requests := awaitPermissions(t, conversation, 2)
	if requests[0].Subject != "bash" || requests[0].Detail != "git push" || requests[1].Detail != "ssh host" {
		t.Fatalf("requests = %+v, %+v", requests[0], requests[1])
	}

	conversation.ResolvePermission(PermissionAllowOnce)
	if result := awaitAuthorization(t, first); !result.allowed || result.err != nil {
		t.Fatalf("first = %+v", result)
	}
	if requests := conversation.Permissions(); len(requests) != 1 || requests[0].Detail != "ssh host" {
		t.Fatalf("pending = %+v", requests)
	}
	conversation.ResolvePermission(PermissionDenyOnce)
	if result := awaitAuthorization(t, second); result.allowed || result.err != nil {
		t.Fatalf("second = %+v", result)
	}
}

func TestAuthorizeNetworkAllowsRemainingSession(t *testing.T) {
	conversation := openTestSession(t, createTestHandle(t), nil)
	defer conversation.Close()

	first := authorizeAsync(context.Background(), conversation, "git push")
	second := authorizeAsync(context.Background(), conversation, "ssh host")
	awaitPermissions(t, conversation, 2)
	conversation.ResolvePermission(PermissionAllowSession)
	for _, done := range []<-chan authorization{first, second} {
		if result := awaitAuthorization(t, done); !result.allowed || result.err != nil {
			t.Fatalf("queued = %+v", result)
		}
	}

	if result := awaitAuthorization(t, authorizeAsync(context.Background(), conversation, "curl host")); !result.allowed || len(conversation.Permissions()) != 0 {
		t.Fatalf("later = %+v, pending = %d", result, len(conversation.Permissions()))
	}
}

func TestInterruptDeniesNetworkAccess(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	conversation := openTestSession(t, createTestHandle(t), testProvider{generate: func(ctx context.Context, _ agent.Request) (agent.Response, error) {
		close(started)
		<-release
		return agent.Response{}, ctx.Err()
	}})
	defer conversation.Close()
	defer close(release)

	pending := authorizeAsync(context.Background(), conversation, "git push")
	awaitPermissions(t, conversation, 1)
	if err := conversation.Send(textContent("prompt")); err != nil {
		t.Fatal(err)
	}
	<-started
	conversation.Interrupt()
	if result := awaitAuthorization(t, pending); result.allowed || result.err != nil {
		t.Fatalf("pending = %+v", result)
	}
	if result := awaitAuthorization(t, authorizeAsync(context.Background(), conversation, "ssh host")); result.allowed || len(conversation.Permissions()) != 0 {
		t.Fatalf("while canceling = %+v, pending = %d", result, len(conversation.Permissions()))
	}
}

func TestAuthorizeNetworkHonorsCancellation(t *testing.T) {
	conversation := openTestSession(t, createTestHandle(t), nil)
	defer conversation.Close()

	ctx, cancel := context.WithCancel(context.Background())
	done := authorizeAsync(ctx, conversation, "ssh host")
	awaitPermissions(t, conversation, 1)
	cancel()
	if result := awaitAuthorization(t, done); !errors.Is(result.err, context.Canceled) {
		t.Fatalf("result = %+v", result)
	}
}
