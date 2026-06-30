# aer-check

`aer-check` finds the metadata and Apex that a Salesforce source tree
**references but does not contain** — the missing pieces that make a repository
*org-dependent*. It is built for org-dependent unlocked package repos, where the
code compiles only against objects, fields, and classes that live in the target
org rather than in the repo.

It drives [`aer`](https://aertest.com/)'s parser, schema loader, and type checker — the same
machinery as `aer test --dry-run` — but instead of running tests it reports the
references it cannot resolve. It can then write a Metadata API `package.xml` for
those components and, given a Salesforce org, fetch them so the package becomes
self-contained.

## Install / build

`aer-check` lives in its own Go module alongside `aer`. The included `go.work`
wires it to the local `aer` checkout:

```
git/
  aer/         # the aer module
  aer-check/   # this module
  force-md/    # aer dependency (local)
```

```sh
cd aer-check
go build -o aer-check .
```

## Usage

```sh
aer-check [directories...] [flags]
```

With no directory, the current directory is analyzed.

| Flag | Description |
|------|-------------|
| `--package-xml <file>` | Write a Metadata API `package.xml` for the missing components. |
| `--fetch-missing <dir>` | Retrieve the missing metadata from an org into `<dir>`. |
| `--account, -a <name>` | Salesforce account/org to fetch from (default: the active org). |
| `--recursive` | After fetching, re-analyze to resolve dependencies of the fetched components. Requires `--fetch-missing`. |
| `--namespace, -d <ns>` | Default namespace for the source (like `aer --default-namespace`). |
| `--package <file>` | Load a managed-package `.pkg` file so the types it provides are not reported as missing (repeatable; like `aer --package`). |
| `--package-dir <dir>` | Load every `.pkg` file in a directory (repeatable; like `aer --package-dir`). |
| `--api-version <v>` | Metadata API version for `package.xml` and fetch (default: the client default). |
| `--verbose, -v` | Print the parse/type-check errors encountered during analysis. |

`aer-check` exits with status `2` when unresolved dependencies remain after
processing, so it can be used as a CI gate. It exits `0` when nothing is
missing and `1` on error.

### Configuration (.aerrc)

`aer-check` reads the same `.aerrc` configuration files as `aer`, so default
flags can be set per project or per user instead of repeating them on every
invocation. Files are read in increasing order of precedence:

1. `$XDG_CONFIG_HOME/aer/aerrc` (or `~/.config/aer/aerrc`)
2. `~/.aerrc`
3. `./.aerrc` (the current directory)

Each line holds one flag, optionally followed by its value; blank lines,
`#` comments, and lines not starting with `-` are ignored. Flags that
`aer-check` does not define (for example `aer`'s `--certificate-dir`) are
ignored, so a project's existing `.aerrc` can be shared between `aer` and
`aer-check`. Flags from `.aerrc` are applied before the command line, so
explicit arguments override them. Set `AER_NO_RC` to ignore all `.aerrc`
files for a run.

```
# .aerrc — defaults for this project
--package-dir packages
--namespace acme
```

### Examples

Report what is missing:

```sh
aer-check force-app/main/default
```

```
Missing dependencies (3):

  Objects (1):
    - Widget__c

  Fields (1):
    - Account.Made_Up_Field__c

  Apex classes (1):
    - HelperUtil
```

Write a manifest of the missing metadata:

```sh
aer-check force-app/main/default --package-xml missing-package.xml
```

Fetch the missing metadata from the default org and keep going until the tree is
self-contained:

```sh
aer-check force-app/main/default \
  --fetch-missing dependencies \
  --recursive
```

Fetch from a specific org:

```sh
aer-check force-app/main/default --fetch-missing dependencies --account my-prod-org
```

Ignore dependencies that a managed package already provides by loading its
`.pkg` files, so only the truly missing components are reported:

```sh
aer-check force-app/main/default \
  --package dlrs.pkg \
  --package-dir ./packages
```

## What it detects

| Kind | How it is referenced | `package.xml` type |
|------|----------------------|--------------------|
| Custom objects | as a type (`Widget__c w`, `new Widget__c()`), in SOQL `FROM`; Profile/PermissionSet `objectPermissions` | `CustomObject` |
| Fields | in SOQL `SELECT`/`WHERE`, `Object.Field` access, formula fields, validation rules; Profile/PermissionSet `fieldPermissions`; layout items; FlexiPage field instances | `CustomField` |
| Apex classes / interfaces | as a type (`new`, variable/parameter/return/cast/`instanceof`); Profile/PermissionSet `classAccesses`; VF page `controller`/`extensions` | `ApexClass` |
| Field sets | Apex `SObjectType.X.FieldSets.Y`, VF `$ObjectType.X.FieldSets.Y` | `FieldSet` |
| Custom metadata records | Apex `Type__mdt.getInstance('X')` | `CustomMetadata` |
| Custom permissions | Profile/PermissionSet `customPermissions`, Apex `FeatureManagement.checkPermission('X')` | `CustomPermission` |
| Custom labels | `Label.My_Label` | `CustomLabel` |
| Flows | `Flow.Interview.My_Flow`; Profile/PermissionSet `flowAccesses` | `Flow` |
| Visualforce pages | `Page.My_Page`, `System.Page.My_Page`; Profile/PermissionSet `pageAccesses` | `ApexPage` |
| Visualforce components | `Component.My_Cmp`, `System.Component.My_Cmp`, page/component markup `<c:cmp>` | `ApexComponent` |
| Static resources | Visualforce page/component markup `$Resource.X` | `StaticResource` |
| Named credentials | Apex callout endpoints (`'callout:My_Cred/...'`) | `NamedCredential` |
| Lightning message channels | LWC `@salesforce/messageChannel/X` import | `LightningMessageChannel` |
| Custom tabs | CustomApplication `<tabs>`, Profile/PermissionSet tab visibilities | `CustomTab` |
| Custom applications | Profile/PermissionSet `applicationVisibilities` | `CustomApplication` |
| Layouts | Profile `layoutAssignments` | `Layout` |
| Record types | Profile/PermissionSet `recordTypeVisibilities` | `RecordType` |
| Global value sets | custom picklist field `valueSetName` | `GlobalValueSet` |
| FlexiPages | object / app action overrides (`<type>Flexipage</type>`), tab `<flexiPage>` | `FlexiPage` |
| Lightning components | FlexiPage `<componentName>c:myCmp</componentName>` | `LightningComponentBundle` |
| Aura components | Aura markup `<c:cmp>`, `extends`, `implements`, and controllers | `AuraDefinitionBundle` |

A field whose parent **object** is also missing is omitted from the report —
retrieving the object brings its fields along. Only **custom** objects and
fields are reported as missing: standard objects, fields, and relationships
(e.g. `RecordType`, `Owner`) and feature- or license-gated standard objects are
always present in the target org and are never fetchable dependencies.
Record-type availability follows the record types defined in the source
metadata. Because only custom metadata is reported, optional schema features do
not need to be enabled for the analysis.

Lightning web component JavaScript is parsed (via the LWC discovery), and its
`@salesforce/...` imports also contribute references: `apex` → Apex classes,
`resourceUrl` → static resources, `messageChannel` → Lightning message channels,
and `schema/Object[.Field]` → custom objects and fields.

### Known limitations

- A class referenced **only** through a static call (`Util.doWork()`) or a bare
  static field (`Util.VALUE`) is reported by the type checker as an ambiguous
  "Variable does not exist", which is indistinguishable from an undefined local
  variable, so it is not classified as a missing class. Reference the type
  directly (e.g. via `new`, a variable, a parameter) to have it detected.
- Custom labels are detected through the `Label.X` form; the three-segment
  `System.Label.X` form is not yet detected.
- Managed-package components are not retrievable through the Metadata API; for a
  managed dependency the fetch step reports a warning and continues.
- Profile/PermissionSet detection covers Apex classes, Visualforce pages, flows,
  and custom permissions; object, field, record-type, and layout references are
  not yet checked.
- Custom permissions are detected through Profile/PermissionSet
  `customPermissions`; the Apex form (`FeatureManagement.checkPermission('X')`)
  is not yet detected.

