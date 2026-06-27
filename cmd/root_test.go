package cmd

import (
	"testing"

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

func TestContainsDir(t *testing.T) {
	dirs := []string{"src", "fetched"}
	if !containsDir(dirs, "fetched") {
		t.Error("expected containsDir to find fetched")
	}
	if containsDir(dirs, "other") {
		t.Error("did not expect containsDir to find other")
	}
}
