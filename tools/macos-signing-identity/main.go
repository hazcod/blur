// Command macos-signing-identity selects an unambiguous local codesigning
// identity for repeatable developer builds. It prints nothing when no safe
// automatic choice exists, leaving Makefile's ad-hoc signing fallback intact.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"runtime"

	"github.com/hazcod/blur/internal/macospackage"
)

func main() {
	app := flag.String("app", "", "existing app bundle used to preserve its signer")
	flag.Parse()
	if runtime.GOOS != "darwin" {
		return
	}
	identities, err := exec.Command("/usr/bin/security", "find-identity", "-v", "-p", "codesigning").CombinedOutput()
	if err != nil {
		return
	}
	var signature []byte
	if *app != "" {
		signature, _ = exec.Command("/usr/bin/codesign", "-dv", "--verbose=4", *app).CombinedOutput()
	}
	if identity := macospackage.SelectSigningIdentity(string(identities), string(signature)); identity != "" {
		_, _ = fmt.Fprintln(os.Stdout, identity)
	}
}
