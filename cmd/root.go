package cmd

import (
	"fmt"
	"os"

	aercmd "github.com/octoberswimmer/aer/cmd"
	"github.com/octoberswimmer/aer/depcheck"
	"github.com/spf13/cobra"
)

// maxRecursiveIterations bounds the fetch/re-analyze loop so a metadata gap
// that never resolves cannot spin forever.
const maxRecursiveIterations = 10

var (
	packageXMLPath string
	fetchDir       string
	account        string
	recursive      bool
	namespace      string
	packageFiles   []string
	packageDirs    []string
	apiVersion     string
	verbose        bool
)

var rootCmd = &cobra.Command{
	Use:   "aer-check [directories...]",
	Short: "Find missing metadata and code dependencies in an Apex source tree",
	Long: `aer-check parses the Apex and metadata in one or more source directories the
same way "aer test --dry-run" does and reports the references it cannot resolve
against the loaded schema and source: missing objects, fields, Apex classes,
custom labels, and flows.

It can write a Metadata API package.xml for the missing components and, with a
Salesforce org, fetch them so a previously org-dependent package becomes
self-contained.`,
	Args:          cobra.ArbitraryArgs,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE:          run,
}

// Execute runs the aer-check command. Default flags from the shared .aerrc
// configuration files are applied before the user's own arguments, so anything
// passed on the command line overrides them.
func Execute() {
	if defaults := aercmd.ConfigDefaultArgs(rootCmd); len(defaults) > 0 {
		rootCmd.SetArgs(append(defaults, os.Args[1:]...))
	}
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "aer-check: %v\n", err)
		os.Exit(1)
	}
}

func init() {
	flags := rootCmd.Flags()
	flags.StringVar(&packageXMLPath, "package-xml", "", "write a package.xml for the missing metadata to this file")
	flags.StringVar(&fetchDir, "fetch-missing", "", "retrieve missing metadata from an org into this directory")
	flags.StringVarP(&account, "account", "a", "", "Salesforce account/org to fetch from (default: active org)")
	flags.BoolVar(&recursive, "recursive", false, "after fetching, re-analyze to resolve dependencies of fetched components")
	flags.StringVarP(&namespace, "namespace", "d", "", "default namespace for the source (like aer --default-namespace)")
	flags.StringArrayVar(&packageFiles, "package", nil, "managed-package .pkg file to load so its types are not reported missing (repeatable)")
	flags.StringArrayVar(&packageDirs, "package-dir", nil, "directory containing .pkg files to load (repeatable)")
	flags.StringVar(&apiVersion, "api-version", "", "Metadata API version for package.xml and fetch (default: client default)")
	flags.BoolVarP(&verbose, "verbose", "v", false, "print parse and type-check errors encountered during analysis")
}

func run(cmd *cobra.Command, args []string) error {
	dirs := args
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	if recursive && fetchDir == "" {
		return fmt.Errorf("--recursive requires --fetch-missing")
	}
	effectiveAPIVersion := apiVersion
	if effectiveAPIVersion == "" {
		effectiveAPIVersion = depcheck.DefaultApiVersion()
	}

	allDirs := append([]string(nil), dirs...)
	union := newDependencySet()
	var lastReport *depcheck.Report

	for iteration := 1; ; iteration++ {
		report, errs, err := depcheck.Analyze(depcheck.Options{
			Dirs:         allDirs,
			Namespace:    namespace,
			PackageFiles: packageFiles,
			PackageDirs:  packageDirs,
		})
		if err != nil {
			return err
		}
		lastReport = report

		if verbose && len(errs) > 0 {
			fmt.Fprintf(os.Stderr, "%d parse/type-check error(s):\n", len(errs))
			for _, e := range errs {
				fmt.Fprintf(os.Stderr, "  %v\n", e)
			}
			fmt.Fprintln(os.Stderr)
		}

		if recursive {
			fmt.Printf("=== Analysis pass %d ===\n", iteration)
		}
		fmt.Println(report.String())
		union.add(report)

		if report.IsEmpty() {
			break
		}
		if fetchDir == "" {
			break
		}

		fmt.Printf("\nFetching %d missing component(s) into %s ...\n", len(report.Missing), fetchDir)
		result, err := depcheck.FetchMissing(report, fetchDir, account, effectiveAPIVersion)
		if err != nil {
			return err
		}
		fmt.Printf("Retrieved %d file(s).\n", len(result.Files))
		for _, p := range result.Problems {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", p)
		}

		if !recursive {
			break
		}
		if !containsDir(allDirs, fetchDir) {
			allDirs = append(allDirs, fetchDir)
		}
		if len(result.Files) == 0 {
			fmt.Println("\nNo new components retrieved; stopping.")
			break
		}
		if iteration >= maxRecursiveIterations {
			fmt.Fprintf(os.Stderr, "\nReached maximum of %d passes; stopping. Re-run to continue.\n", maxRecursiveIterations)
			break
		}
	}

	if packageXMLPath != "" {
		unionReport := union.report()
		if err := os.WriteFile(packageXMLPath, []byte(unionReport.PackageXML(effectiveAPIVersion)), 0o644); err != nil {
			return fmt.Errorf("failed to write %s: %w", packageXMLPath, err)
		}
		fmt.Printf("\nWrote package.xml (%d component(s)) to %s\n", len(unionReport.Missing), packageXMLPath)
	}

	// Exit non-zero when the source still has unresolved dependencies, so the
	// command works as a CI gate.
	if lastReport != nil && !lastReport.IsEmpty() {
		os.Exit(2)
	}
	return nil
}

func containsDir(dirs []string, dir string) bool {
	for _, d := range dirs {
		if d == dir {
			return true
		}
	}
	return false
}

// dependencySet accumulates a deduplicated union of dependencies across passes.
type dependencySet struct {
	seen  map[string]struct{}
	items []depcheck.Dependency
}

func newDependencySet() *dependencySet {
	return &dependencySet{seen: make(map[string]struct{})}
}

func (s *dependencySet) add(report *depcheck.Report) {
	if report == nil {
		return
	}
	for _, d := range report.Missing {
		key := d.MetadataType + "\x00" + d.Member
		if _, ok := s.seen[key]; ok {
			continue
		}
		s.seen[key] = struct{}{}
		s.items = append(s.items, d)
	}
}

func (s *dependencySet) report() *depcheck.Report {
	return &depcheck.Report{Missing: s.items}
}
