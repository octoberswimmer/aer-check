package cmd

import (
	"fmt"
	"os"
	"strings"

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
	typeFilter     []string
	references     bool
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
	flags.StringArrayVarP(&typeFilter, "type", "t", nil, "only report this Metadata API type, e.g. CustomField or ApexPage (repeatable)")
	flags.BoolVar(&references, "references", false, "list the source files that reference each missing item")
}

// validateTypeFilter warns about any requested type that is not a Metadata API
// type aer-check can report, so a typo does not silently match nothing.
func validateTypeFilter(types []string) {
	if len(types) == 0 {
		return
	}
	known := make(map[string]string)
	for _, name := range depcheck.MetadataTypeNames() {
		known[strings.ToLower(name)] = name
	}
	for _, t := range types {
		if _, ok := known[strings.ToLower(strings.TrimSpace(t))]; !ok {
			fmt.Fprintf(os.Stderr, "aer-check: warning: %q is not a known metadata type; known types: %s\n",
				t, strings.Join(depcheck.MetadataTypeNames(), ", "))
		}
	}
}

func run(cmd *cobra.Command, args []string) error {
	dirs := args
	if len(dirs) == 0 {
		dirs = []string{"."}
	}
	if recursive && fetchDir == "" {
		return fmt.Errorf("--recursive requires --fetch-missing")
	}
	validateTypeFilter(typeFilter)
	effectiveAPIVersion := apiVersion
	if effectiveAPIVersion == "" {
		effectiveAPIVersion = depcheck.DefaultApiVersion()
	}

	allDirs := append([]string(nil), dirs...)
	union := newDependencySet()
	var lastReport *depcheck.Report
	// Components that could not be retrieved, keyed so they are not requested
	// again on a later pass and are kept for the final summary.
	unretrievable := make(map[string]depcheck.Dependency)
	// Components requested on the previous pass; any still missing this pass
	// could not be retrieved.
	requested := make(map[string]struct{})

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
		report = report.FilterByTypes(typeFilter)
		lastReport = report

		// A component requested on the previous pass that is still missing was
		// not retrieved (the org may not report a warning for every such case).
		for _, d := range report.Missing {
			if _, req := requested[d.Key()]; req {
				unretrievable[d.Key()] = d
			}
		}

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
		if references {
			fmt.Println(report.StringWithReferences())
		} else {
			fmt.Println(report.String())
		}
		union.add(report)

		if report.IsEmpty() {
			break
		}
		if fetchDir == "" {
			break
		}

		// Only fetch components not already known to be unretrievable.
		fetchReport := &depcheck.Report{}
		nextRequested := make(map[string]struct{})
		for _, d := range report.Missing {
			if _, failed := unretrievable[d.Key()]; failed {
				continue
			}
			fetchReport.Missing = append(fetchReport.Missing, d)
			nextRequested[d.Key()] = struct{}{}
		}
		if fetchReport.IsEmpty() {
			fmt.Println("\nRemaining components cannot be retrieved from the org; stopping.")
			break
		}

		fmt.Printf("\nFetching %d missing component(s) into %s ...\n", len(fetchReport.Missing), fetchDir)
		result, err := depcheck.FetchMissing(fetchReport, fetchDir, account, effectiveAPIVersion)
		if err != nil {
			return err
		}
		fmt.Printf("Retrieved %d file(s).\n", len(result.Files))
		for _, p := range result.Problems {
			fmt.Fprintf(os.Stderr, "  warning: %s\n", p)
		}
		// The org may report some unretrievable components explicitly; record
		// those too so they are summarized even without another pass.
		for _, d := range fetchReport.UnretrievableFrom(result.Problems) {
			unretrievable[d.Key()] = d
		}
		requested = nextRequested

		if !recursive {
			break
		}
		if !containsDir(allDirs, fetchDir) {
			allDirs = append(allDirs, fetchDir)
		}
		if iteration >= maxRecursiveIterations {
			fmt.Fprintf(os.Stderr, "\nReached maximum of %d passes; stopping. Re-run to continue.\n", maxRecursiveIterations)
			break
		}
	}

	if len(unretrievable) > 0 {
		ur := &depcheck.Report{}
		for _, d := range unretrievable {
			ur.Missing = append(ur.Missing, d)
		}
		fmt.Printf("\n%d component(s) could not be retrieved from the org:\n", len(ur.Missing))
		fmt.Println(ur.StringWithReferences())
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
