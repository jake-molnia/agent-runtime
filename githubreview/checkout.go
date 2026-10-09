package githubreview

import (
	"context"
	"errors"
	"path/filepath"
	"strings"

	"github.com/jake-molnia/agent-runtime/sandbox"
	process "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

// PrepareRepository creates an exact detached checkout using a transient contents:read
// token. The trusted deployment directory must belong to this disposable sandbox.
// The returned hook never forwards provider secrets to the checkout process.
func PrepareRepository(input Input, token, directory string) (func(context.Context, *sandbox.Runtime, map[string]string) error, error) {
	if err := validateInput(input); err != nil {
		return nil, err
	}
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return nil, errors.New("repository checkout token required")
	}
	if !filepath.IsAbs(directory) || filepath.Clean(directory) != directory || directory == "/" || strings.ContainsAny(directory, "\x00\\") {
		return nil, errors.New("repository checkout requires a clean absolute sandbox directory")
	}
	return func(ctx context.Context, runtime *sandbox.Runtime, _ map[string]string) error {
		if runtime == nil || runtime.Processes == nil {
			return errors.New("repository checkout runtime unavailable")
		}
		cwd := "/"
		response, err := runtime.Processes.Execute(ctx, &process.ExecuteRequest{Config: &process.ProcessConfig{
			Command: []string{"python3", "-c", checkoutScript, input.Repository, input.BaseSHA, input.HeadSHA, directory},
			Cwd:     &cwd, EnvVars: map[string]string{"AGENT_RUNTIME_CHECKOUT_TOKEN": token},
		}})
		if err != nil {
			return errors.New("repository checkout transport failed")
		}
		if response == nil || response.ExitCode != 0 {
			return errors.New("repository checkout failed")
		}
		return nil
	}, nil
}

const checkoutScript = `
import json, os, pathlib, signal, subprocess, sys, tempfile

signal.signal(signal.SIGTERM, lambda signum, frame: sys.exit(128 + signum))
signal.signal(signal.SIGINT, lambda signum, frame: sys.exit(128 + signum))
repository, base, head, destination = sys.argv[1:]
root = pathlib.Path(destination)
if root.resolve() != root:
    raise RuntimeError("checkout directory contains symlinks")
root.mkdir(parents=True, exist_ok=True)
identity = json.dumps([repository, base, head])
metadata = root / ".git"
marker = metadata / "agent-runtime-checkout"
if metadata.is_symlink() or (metadata.exists() and not metadata.is_dir()):
    raise RuntimeError("checkout metadata is not a directory")
if marker.exists():
    if marker.is_symlink() or marker.read_text() != identity:
        raise RuntimeError("checkout identity differs")
elif any(path.name != ".git" for path in root.iterdir()):
    raise RuntimeError("checkout directory is not empty")

scratch = next(path for path in ("/tmp", "/var/tmp") if not pathlib.Path(path).is_relative_to(root))
with tempfile.TemporaryDirectory(prefix="agent-checkout-", dir=scratch) as temporary:
    home = pathlib.Path(temporary)
    token = home / "token"
    token.write_text(os.environ.pop("AGENT_RUNTIME_CHECKOUT_TOKEN"))
    token.chmod(0o600)
    askpass = home / "askpass"
    askpass.write_text('#!/bin/sh\ncase "$1" in *Username*) printf "%s\\n" x-access-token;; *) cat "$AGENT_RUNTIME_CHECKOUT_TOKEN_FILE";; esac\n')
    askpass.chmod(0o700)
    env = {"PATH": os.environ.get("PATH", os.defpath), "HOME": temporary,
           "GIT_CONFIG_NOSYSTEM": "1", "GIT_CONFIG_GLOBAL": "/dev/null",
           "GIT_TERMINAL_PROMPT": "0", "GIT_ASKPASS": str(askpass),
           "AGENT_RUNTIME_CHECKOUT_TOKEN_FILE": str(token)}
    def git(*args):
        return subprocess.run(["git", "-c", "core.hooksPath=/dev/null",
                               "-c", "credential.helper=", "-c", "http.followRedirects=false",
                               "-c", "protocol.file.allow=never", "-c", "protocol.ext.allow=never",
                               *args], cwd=root, env=env, check=True,
                              stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=120).stdout.decode().strip()
    if not marker.exists():
        git("init", "--template=", ".")
        with tempfile.NamedTemporaryFile(mode="w", dir=metadata, prefix="agent-runtime-checkout-", delete=False) as pending:
            pending.write(identity)
        pathlib.Path(pending.name).replace(marker)
    url = "https://github.com/" + repository + ".git"
    git("fetch", "--no-tags", "--depth=1000", url, base, head)
    if git("rev-parse", "--verify", base + "^{commit}") != base:
        raise RuntimeError("base commit mismatch")
    git("checkout", "--detach", "--force", head)
    if git("rev-parse", "HEAD") != head:
        raise RuntimeError("head commit mismatch")
`
