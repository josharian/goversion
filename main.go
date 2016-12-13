// goversion is a tool to install and use multiple Go versions.

package main

import (
	"archive/zip"
	"bufio"
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const (
	remote    = "https://go.googlesource.com/go"
	release14 = "release-branch.go1.4"
	debug     = true
)

// list prints the available tagged releases.
func list() {
	cmd := exec.Command("git", "ls-remote", "--tags", remote, "go1*")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatal(err)
	}
	scan := bufio.NewScanner(bytes.NewReader(out))
	for scan.Scan() {
		line := scan.Text()
		ff := strings.Fields(line)
		if len(ff) != 2 {
			log.Fatalf("unexpected git ls-remote line %q", line)
		}
		fmt.Println(strings.TrimPrefix(ff[1], "refs/tags/"))
	}
}

func listdl() {
	resp, err := http.Get("https://storage.googleapis.com/go-builder-data/dl-index.txt")
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	scan := bufio.NewScanner(resp.Body)
	nosuffix := strings.NewReplacer(".tar.gz", "", ".zip", "")
	targetos := runtime.GOOS
	targetarch := runtime.GOARCH
	for scan.Scan() {
		// Example line:
		// https://storage.googleapis.com/golang/go1.2.2.darwin-386-osx10.6.tar.gz
		line := scan.Text()
		// Ignore downloads that we can't use directly.
		if strings.HasSuffix(line, ".pkg") ||
			strings.HasSuffix(line, ".msi") ||
			strings.HasSuffix(line, ".sha256") ||
			strings.HasSuffix(line, ".src.tar.gz") ||
			!strings.Contains(line, targetos) {
			continue
		}
		// Strip down to just the filename.
		// go1.2.2.darwin-386-osx10.6.tar.gz
		i := strings.LastIndexByte(line, '/')
		if i == -1 {
			continue
		}
		line = line[i+1:]
		// Eliminate file suffixes.
		// go1.2.2.darwin-386-osx10.6
		line = nosuffix.Replace(line)
		// Break up remainder into version and platform.
		// The pattern is version.platform, but platform can contain periods.
		// Instead, split on GOOS.
		// go1.2.2 and darwin-386-osx10.6
		i = strings.Index(line, targetos)
		vers, plat := line[:i-1], line[i:]
		// Platform can contain two or three components.
		// If two, GOOS and GOARCH.
		// If three, GOOS, GOARCH, sub-GOARCH.
		// We know GOOS matches.
		// GOARCH and sub-GOARCH have a lot of variation.
		platx := strings.Split(plat, "-")
		switch len(platx) {
		default:
			continue // Not the droid we're looking for.
		case 3:
			// Only happens with darwin.
			// Assume no-one runs 10.6 anymore.
			if platx[2] == "osx10.6" {
				continue
			}
			platx = platx[:2]
		case 2:
			// Continued below.
		}
		arch := platx[1]
		// Clean up arch.
		// go1.6beta1 has linux-arm and linux-arm6 downloads.
		// Every other release has armv6l.
		// Skip plain arm and then map arm6 and armv6l to arm, to match GOARCH naming.
		switch arch {
		case "arm":
			continue
		case "arm6", "armv6l":
			arch = "arm"
		}
		if arch != targetarch {
			continue
		}
		fmt.Println(vers)
	}
	if scan.Err() != nil {
		log.Fatal(err)
	}
}

// repoParent returns the parent directory of the Go repo(s).
func repoParent() string {
	cmd := exec.Command("go", "env", "GOPATH")
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("could not determine repo path: %v", err)
	}
	gopath := strings.TrimSpace(string(out))
	list := filepath.SplitList(gopath)
	if len(list) == 0 {
		log.Fatalf("could not determine repo path: could not parse GOPATH=%q", gopath)
	}
	return filepath.Join(list[0], "src", "golang.org", "x")
}

func cmdgo(parent, ref string) (path string, exist bool) {
	e := "go"
	if runtime.GOOS == "windows" {
		e = "go.exe"
	}
	path = filepath.Join(parent, ref, "bin", e)
	_, err := os.Stat(path)
	return path, !os.IsNotExist(err)
}

// update clones or updates the Go repo.
func update() {
	parent := repoParent()
	path := filepath.Join(parent, "go.mirror")
	var cmd *exec.Cmd
	var verb, gerund string
	if _, err := os.Stat(path); os.IsNotExist(err) {
		// Clone repo.
		cmd = exec.Command("git", "clone", "--bare", remote, path)
		verb = "clone"
		gerund = "cloning"
	} else {
		cmd = exec.Command("git", "fetch")
		cmd.Dir = path
		verb = "update"
		gerund = "updating"
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	log.Printf("%s Go repo", gerund)
	if err := cmd.Run(); err != nil {
		log.Fatalf("could not %s Go repo: %v", verb, err)
	}
}

func export(ref string) {
	parent := repoParent()

	// Manually resolve ref to provide better error messages if it is bogus.
	cmd := exec.Command("git", "rev-parse", ref)
	cmd.Dir = filepath.Join(parent, "go.mirror")
	if err := cmd.Run(); err != nil {
		log.Fatalf("could not resolve %q: %v", ref, err)
	}

	// Use git archive to generate a zip file at ref.
	zipfile := filepath.Join(parent, ref+".zip")
	cmd = exec.Command("git", "archive", "--format", "zip", "-o", zipfile, ref)
	cmd.Dir = filepath.Join(parent, "go.mirror")
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	// log.Printf("generating zip from Go repo at %s", ref)
	if err := cmd.Run(); err != nil {
		log.Fatalf("could not archive Go repo: %v", err)
	}
	defer os.Remove(zipfile)

	// Expand the zipfile.
	r, err := zip.OpenReader(zipfile)
	if err != nil {
		log.Fatal("could not open zip: %v", err)
	}
	defer r.Close()

	root := filepath.Join(parent, ref)
	if err := os.Mkdir(root, 0755); err != nil && !os.IsExist(err) {
		log.Fatalf("could not mkdir %s: %v", root, err)
	}

	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			log.Fatal("could not read zip file entry %s", f.Name)
		}
		outpath := filepath.Join(root, f.Name)
		if f.FileInfo().IsDir() {
			// Directory
			os.MkdirAll(outpath, f.Mode())
			rc.Close()
			continue
		}
		// File
		os.MkdirAll(filepath.Dir(outpath), f.Mode())
		out, err := os.OpenFile(outpath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
		if err != nil {
			log.Fatalf("could not create file %s: %v", outpath, err)
		}
		_, err = io.Copy(out, rc)
		if err != nil {
			log.Fatalf("could not write to file %s: %v", outpath, err)
		}
		out.Close()
		rc.Close()
	}

	vfp := filepath.Join(root, "VERSION")
	vf, err := os.OpenFile(vfp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		log.Fatalf("could not create VERSION file: %v", err)
	}
	if _, err := io.WriteString(vf, ref+"\n"); err != nil {
		os.Remove(vfp)
		log.Fatalf("could not write VERSION file: %v", err)
	}
	vf.Close()
}

func make(ref string) {
	// Check whether we need a C compiler, and if so, whether we have one.
	if os.Getenv("CGO_ENABLED") != "0" {
		var havecc bool
		ccs := []string{"gcc", "clang"}
		if cc := os.Getenv("CC"); cc != "" {
			ccs = append(ccs, cc)
		}
		for _, cc := range ccs {
			if cc == "" {
				continue
			}
			if _, err := exec.LookPath(cc); err != nil {
				continue
			}
			havecc = true
			break
		}
		if !havecc {
			log.Fatalf("could not find a C compiler, tried %s", ccs)
		}
	}
	parent := repoParent()
	srcdir := filepath.Join(parent, ref, "src")
	var script string
	switch runtime.GOOS {
	case "darwin", "linux", "freebsd", "netbsd", "openbsd", "dragonfly":
		script = "make.bash"
	case "windows": // won't work without gcc
		script = "make.bat"
	case "plan9":
		script = "make.rc"
	default:
		log.Fatalf("unrecognized GOOS: %s", runtime.GOOS)
	}
	mk, err := filepath.Abs(filepath.Join(parent, ref, "src", script))
	if err != nil {
		log.Fatalf("could not get absolute path to %s in %s: %v", script, srcdir, err)
	}
	cmd := exec.Command(mk)
	cmd.Dir = srcdir
	log.Printf("running %s", mk)
	out, err := cmd.CombinedOutput()
	if err != nil {
		log.Fatalf("could not build %s: %v\n\n%s", ref, err, out)
	}
	// Confirm that cmd/go got build.
	// make.bat doesn't set its return code correctly
	// in (at a minimum) all versions up to 1.8.1beta.
	if _, exist := cmdgo(parent, ref); !exist {
		log.Fatalf("could not find cmd/go:\n\n%s", out)
	}
	// if runtime.GOOS != "windows" build was successful
	if runtime.GOOS != "windows" {
		return
	}

	// workaround
	// on windows: make.bat will silently fail, hopefully good-enough workaround: check for bin\go.exe
	goexe := filepath.Join(parent, ref, "bin", "go.exe")
	_, err = exec.LookPath(goexe)
	if err != nil {
		log.Fatalf("go.exe is not available for %s: %v", ref, err)
	}
}

// getdlindex() returns downloadable go binary list
// source: https://storage.googleapis.com/go-builder-data/dl-index.txt
func getdlindex() (string, error) {
	resp, err := http.Get("https://storage.googleapis.com/go-builder-data/dl-index.txt")
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// selectBinary() builds download url from running program context
// returns url prefix and file name otherwise error
func selectBinary() (string, string, error) {
	err := error(nil)

	dlindex, err := getdlindex()
	if err != nil {
		return "", "", err
	}

	ref, _ := version(flag.Arg(1))

	ext := ""
	switch runtime.GOOS {
	case "darwin":
		ext = "-osx10.6.pkg" // TODO(fgergo): ask brad(?) how to handle 1.6 vs. 1.8 binaries
	case "linux":
		ext = ".tar.gz"
	case "windows":
		ext = ".zip"
	default:
		err = errors.New("unrecognized GOOS: " + runtime.GOOS)
	}
	file := ref + "." + runtime.GOOS + "-" + runtime.GOARCH + ext
	url := "https://storage.googleapis.com/golang/" + file
	if strings.Index(dlindex, url) == -1 {
		return "", "", errors.New(fmt.Sprintf("binary (%s) not available", url))
	}

	return "https://storage.googleapis.com/golang/", file, err
}

// download() downloads and saves go binary install package ver
// from remoteBinary to os.TempDir(), returns file path
func download() (string, error) {
	url, file, err := selectBinary()
	if err != nil {
		return "", err
	}

	if debug {
		log.Printf("downloading %s\n", url+file)
	}

	resp, err := http.Get(url + file)
	if err != nil {
		return "", err
	}

	defer resp.Body.Close()
	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	path := os.TempDir()
	err = ioutil.WriteFile(filepath.Join(path, file), body, os.ModeAppend)
	if err != nil {
		return "", err
	}

	return filepath.Join(path, file), nil
}

const usage = `goversion is a tool to install and use multiple Go versions.

Usage:

        goversion list                  list known Go versions
        goversion install <version>     install a Go version
        goversion <version> <args>      run 'go args' using a given Go version

For example:

goversion install 1.8beta1
goversion 1.8beta1 test ./...

`

func printUsage() {
	fmt.Fprint(os.Stderr, usage)
	os.Exit(2)
}

// version converts versions to have a go prefix and reports whether it looks like a go version.
// For example, go1.7.4 and 1.7.4 both return go1.7.4, true.
func version(s string) (string, bool) {
	// Accept both go1.7.4 and 1.7.4.
	s = strings.TrimPrefix(s, "go")
	if !strings.HasPrefix(s, "1") {
		return "", false
	}
	return "go" + s, true
}

func main() {
	log.SetFlags(0)
	flag.Parse()

	if flag.NArg() < 1 {
		printUsage()
	}

	switch flag.Arg(0) {
	case "list":
		list()
		return
	case "listdl":
		listdl()
		return
	case "update":
		// Intentionally undocumented, useful during testing.
		update()
		return
	case "export":
		// Intentionally undocumented, useful during testing.
		update()
		if flag.NArg() < 2 {
			printUsage()
		}
		ref := flag.Arg(1)
		export(ref)
		return
	case "download":
		// Intentionally undocumented, useful during testing.
		path, err := download() // TODO(fgergo): remove, when ready
		if err != nil {
			log.Fatalf("download error: %s", err)
		}
		log.Printf("download ready: %s", path)
		return
	case "unpack":
		// Intentionally undocumented, useful during testing.
		return
	case "install":
		update()
		if flag.NArg() < 2 {
			printUsage()
		}
		ref, ok := version(flag.Arg(1))
		if !ok {
			printUsage()
		}

		parent := repoParent()
		bootstrap := filepath.Join(parent, release14)
		_, exist := cmdgo(parent, release14)
		if !exist {
			export(release14)
			make(release14)
		}
		os.Setenv("GOROOT_BOOTSTRAP", bootstrap)

		export(ref)
		make(ref)
		return
	}

	ref, ok := version(flag.Arg(0))
	if !ok {
		printUsage()
	}

	// Execute command with the requested version.
	parent := repoParent()
	path, exist := cmdgo(parent, ref)
	if !exist {
		log.Fatalf("%s not found. Have you run %s install %s?", path, os.Args[0], ref)
	}
	cmd := exec.Command(path, flag.Args()[1:]...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		// TODO: Attempt to replicate original rc?
		os.Exit(1)
	}
}
