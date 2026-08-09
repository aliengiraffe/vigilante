// Package scripts contains no Go implementation. It exists so that the shell
// scripts in this directory — release signing, binary installation, daemon setup,
// nightly cask updates, and coverage reporting — can be covered by Go tests that
// execute them as subprocesses and assert on their observable behavior.
//
// The scripts are the deliverable; the Go files here are their test harness.
package scripts
