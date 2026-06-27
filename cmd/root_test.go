package cmd

import (
	"os"
	"path/filepath"
	"testing"

	aercmd "github.com/octoberswimmer/aer/cmd"
	"github.com/octoberswimmer/aer/depcheck"
)

func TestDependencySetDeduplicatesAcrossPasses(t *testing.T) {
	set := newDependencySet()
	set.add(&depcheck.Report{Missing: []depcheck.Dependency{
		{MetadataType: "CustomObject", Member: "Widget__c"},
		{MetadataType: "ApexClass", Member: "HelperUtil"},
	}})
	// A later pass surfaces the same object plus a new field.
	set.add(&depcheck.Report{Missing: []depcheck.Dependency{
		{MetadataType: "CustomObject", Member: "Widget__c"},
		{MetadataType: "CustomField", Member: "Account.Score__c"},
	}})

	got := set.report()
	if len(got.Missing) != 3 {
		t.Fatalf("expected 3 unique dependencies, got %d: %+v", len(got.Missing), got.Missing)
	}
}

func TestAerrcDefaultsFilterToAerCheckFlags(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("AER_NO_RC", "")

	cfgDir := filepath.Join(home, ".config", "aer")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// --package-dir is an aer-check flag; --certificate-dir is aer-only.
	contents := "--package-dir packages\n--certificate-dir certs\n"
	if err := os.WriteFile(filepath.Join(cfgDir, "aerrc"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	got := aercmd.ConfigDefaultArgs(rootCmd)
	want := []string{"--package-dir", "packages"}
	if len(got) != len(want) {
		t.Fatalf("ConfigDefaultArgs() = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ConfigDefaultArgs() = %#v, want %#v", got, want)
		}
	}
}

func TestContainsDir(t *testing.T) {
	dirs := []string{"src", "fetched"}
	if !containsDir(dirs, "fetched") {
		t.Error("expected containsDir to find fetched")
	}
	if containsDir(dirs, "other") {
		t.Error("did not expect containsDir to find other")
	}
}
