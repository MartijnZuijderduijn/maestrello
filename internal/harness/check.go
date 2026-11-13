package harness

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"maestrello/internal/textutil"
)

// versionTimeout bounds `<bin> --version`.
const versionTimeout = 5 * time.Second

// checkHarnessBinary resolves bin and runs `<bin> --version`.
func CheckBinary(bin string) (version string, err error) {
	if bin == "" {
		return "", fmt.Errorf("no binary configured")
	}
	path := bin
	if bin == filepath.Base(bin) {
		p, lerr := exec.LookPath(bin)
		if lerr != nil {
			return "", fmt.Errorf("%q not found in PATH", bin)
		}
		path = p
	} else if st, serr := os.Stat(bin); serr != nil || st.IsDir() {
		return "", fmt.Errorf("%s does not exist", bin)
	}
	ctx, cancel := context.WithTimeout(context.Background(), versionTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Env = os.Environ()
	out, rerr := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s --version timed out after %s", bin, versionTimeout)
	}
	if rerr != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			return "", fmt.Errorf("%s --version: %v", bin, rerr)
		}
		return "", fmt.Errorf("%s --version: %v: %s", bin, rerr, textutil.Tail(msg, 200))
	}
	v := strings.TrimSpace(textutil.FirstLine(string(out), 200))
	if v == "" {
		v = "(no version output)"
	}
	return v, nil
}
