package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type factoryFixture struct {
	ctx     context.Context
	dir     string
	binary  string
	current string
	applied bool
}

func compileFixture(ctx context.Context, fixture, name string) (*factoryFixture, error) {
	root, err := filepath.Abs("../../../../..")
	if err != nil {
		return nil, err
	}
	base := filepath.Join(root, "_output/integration")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp(base, name+"-")
	if err != nil {
		return nil, err
	}
	cli := os.Getenv("UNOBIN")
	if cli == "" {
		cli = filepath.Join(base, "bin/unobin")
	}
	cmd := exec.CommandContext(ctx, cli, "compile", "-p", fixture, "-o", work,
		"--name", name, "--replace-go-module",
		"github.com/cloudboss/unobin-library-kubernetes="+root, "--build")
	if output, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("compile fixture (retained at %s): %w\n%s", work, err, output)
	}
	return &factoryFixture{ctx: ctx, dir: work, binary: filepath.Join(work, name),
		current: "config.ub"}, nil
}

func (f *factoryFixture) run(args ...string) ([]byte, error) {
	cmd := exec.CommandContext(f.ctx, f.binary, args...)
	cmd.Dir = f.dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("factory %v: %w\n%s", args, err, output)
	}
	return output, nil
}

func (f *factoryFixture) commands(commands ...[]string) error {
	for _, args := range commands {
		if _, err := f.run(args...); err != nil {
			return err
		}
	}
	return nil
}

func (f *factoryFixture) config(source, target string, replace *strings.Replacer) error {
	body, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	path := filepath.Join(f.dir, target)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(replace.Replace(string(body))), 0o600); err != nil {
		return err
	}
	_, err = f.run("pin", "-c", target)
	return err
}

func (f *factoryFixture) cleanup(result *error) {
	if f.applied {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		f.ctx = ctx
		*result = errors.Join(*result, f.commands(
			[]string{"plan", "--destroy", "-c", f.current, "-o", "destroy.ubp"},
			[]string{"apply", "destroy.ubp"},
		))
	}
	if *result != nil {
		fmt.Fprintf(os.Stderr, "factory state retained at %s\n", f.dir)
	} else {
		*result = os.RemoveAll(f.dir)
	}
}
