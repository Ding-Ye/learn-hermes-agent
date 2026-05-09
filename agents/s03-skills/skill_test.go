package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubstituteVars_KnownReplaced(t *testing.T) {
	env := &Env{
		WorkingDir: "/tmp/wd",
		SkillsDir:  "/skills",
		SkillInput: "input.txt",
	}
	out := substituteVars("hi from ${HERMES_WORKING_DIR}, file=${HERMES_SKILL_INPUT}", env)
	want := "hi from /tmp/wd, file=input.txt"
	if out != want {
		t.Fatalf("got %q, want %q", out, want)
	}
}

func TestSubstituteVars_UnknownPreserved(t *testing.T) {
	env := &Env{}
	out := substituteVars("known=${HERMES_WORKING_DIR}, unknown=${UNDEFINED}", env)
	if !strings.Contains(out, "${UNDEFINED}") {
		t.Fatalf("unknown var should be preserved, got %q", out)
	}
}

func TestExpandInlineShell_SingleCommand(t *testing.T) {
	out, err := expandInlineShell(context.Background(), "answer=`echo 42`")
	if err != nil {
		t.Fatal(err)
	}
	if out != "answer=42" {
		t.Fatalf("got %q, want %q", out, "answer=42")
	}
}

func TestExpandInlineShell_MultipleCommands(t *testing.T) {
	out, err := expandInlineShell(context.Background(), "a=`echo 1` b=`echo 2`")
	if err != nil {
		t.Fatal(err)
	}
	if out != "a=1 b=2" {
		t.Fatalf("got %q", out)
	}
}

func TestExpand_VarsBeforeShell(t *testing.T) {
	env := &Env{SkillInput: "/tmp/xx"}
	s := &Skill{BodyTemplate: "value=`echo ${HERMES_SKILL_INPUT}`"}
	out, err := s.Expand(context.Background(), env)
	if err != nil {
		t.Fatal(err)
	}
	if out != "value=/tmp/xx\n" && out != "value=/tmp/xx" {
		// The body has no trailing newline, but Expand preserves the
		// template's trailing newline if any. Our template here has none.
		t.Fatalf("got %q — vars must be substituted before shell runs", out)
	}
}

func TestLoadSkills_ParsesValidFile(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "hi.md"), `---
name: hi
description: A simple greeting skill.
---
Hello, ${HERMES_WORKING_DIR}!
`)
	mustWrite(t, filepath.Join(dir, "README.md"), "# not a skill")

	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 1 {
		t.Fatalf("got %d skills, want 1", len(skills))
	}
	got := skills[0]
	if got.Name != "hi" || got.Description != "A simple greeting skill." {
		t.Fatalf("metadata wrong: %+v", got)
	}
	if !strings.Contains(got.BodyTemplate, "Hello, ${HERMES_WORKING_DIR}!") {
		t.Fatalf("body wrong: %q", got.BodyTemplate)
	}
}

func TestLoadSkills_RejectsMissingFrontmatter(t *testing.T) {
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "bad.md"), "no frontmatter here\n")
	skills, err := LoadSkills(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(skills) != 0 {
		t.Fatalf("expected to skip the malformed file, got %d skills", len(skills))
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
