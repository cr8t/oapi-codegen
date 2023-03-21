// Copyright 2019 DeepMap, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime/debug"
	"strings"

	"gopkg.in/yaml.v2"

	"github.com/deepmap/oapi-codegen/pkg/codegen"
	"github.com/deepmap/oapi-codegen/pkg/util"
)

func errExit(format string, args ...interface{}) {
	_, _ = fmt.Fprintf(os.Stderr, format, args...)
	os.Exit(1)
}

var (
	flagOutputFile     string
	flagConfigFile     string
	flagOldConfigStyle bool
	flagOutputConfig   bool
	flagPrintVersion   bool
	flagPackageName    string
	flagPrintUsage     bool
	flagGenerate       string
	flagTemplatesDir   string

	// Deprecated: The options below will be removed in a future
	// release. Please use the new config file format.
	flagIncludeTags        string
	flagExcludeTags        string
	flagImportMapping      string
	flagExcludeSchemas     string
	flagResponseTypeSuffix string
	flagAliasTypes         bool
)

type configuration struct {
	codegen.Configuration `yaml:",inline"`

	// OutputFile is the filename to output.
	OutputFile string `yaml:"output,omitempty"`
}

// oldConfiguration is deprecated. Please add no more flags here. It is here
// for backwards compatibility, and it will be removed in the future.
type oldConfiguration struct {
	PackageName        string                       `yaml:"package"`
	GenerateTargets    []string                     `yaml:"generate"`
	OutputFile         string                       `yaml:"output"`
	IncludeTags        []string                     `yaml:"include-tags"`
	ExcludeTags        []string                     `yaml:"exclude-tags"`
	TemplatesDir       string                       `yaml:"templates"`
	ImportMapping      map[string]string            `yaml:"import-mapping"`
	ExcludeSchemas     []string                     `yaml:"exclude-schemas"`
	ResponseTypeSuffix string                       `yaml:"response-type-suffix"`
	Compatibility      codegen.CompatibilityOptions `yaml:"compatibility"`
}

func main() {
	flag.StringVar(&flagOutputFile, "o", "", "Where to output generated code, stdout is default")
	flag.BoolVar(&flagOldConfigStyle, "old-config-style", false, "whether to use the older style config file format")
	flag.BoolVar(&flagOutputConfig, "output-config", false, "when true, outputs a configuration file for oapi-codegen using current settings")
	flag.StringVar(&flagConfigFile, "config", "", "a YAML config file that controls oapi-codegen behavior")
	flag.BoolVar(&flagPrintVersion, "version", false, "when specified, print version and exit")
	flag.StringVar(&flagPackageName, "package", "", "The package name for generated code")
	flag.BoolVar(&flagPrintUsage, "help", false, "show this help and exit")
	flag.BoolVar(&flagPrintUsage, "h", false, "same as -help")

	// All flags below are deprecated, and will be removed in a future release. Please do not
	// update their behavior.
	flag.StringVar(&flagGenerate, "generate", "types,client,server,spec",
		`Comma-separated list of code to generate; valid options: "types", "client", "chi-server", "server", "gin", "gorilla", "kit", "spec", "skip-fmt", "skip-prune"`)
	flag.StringVar(&flagIncludeTags, "include-tags", "", "Only include operations with the given tags. Comma-separated list of tags.")
	flag.StringVar(&flagExcludeTags, "exclude-tags", "", "Exclude operations that are tagged with the given tags. Comma-separated list of tags.")
	flag.StringVar(&flagTemplatesDir, "templates", "", "Path to directory containing user templates")
	flag.StringVar(&flagImportMapping, "import-mapping", "", "A dict from the external reference to golang package path")
	flag.StringVar(&flagExcludeSchemas, "exclude-schemas", "", "A comma separated list of schemas which must be excluded from generation")
	flag.StringVar(&flagResponseTypeSuffix, "response-type-suffix", "", "the suffix used for responses types")
	flag.BoolVar(&flagAliasTypes, "alias-types", false, "Alias type declarations of possible")

	flag.Parse()

	if flagPrintUsage {
		flag.Usage()
		os.Exit(0)
	}

	if flagPrintVersion {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			fmt.Fprintln(os.Stderr, "error reading build info")
			os.Exit(1)
		}
		fmt.Println(bi.Main.Path + "/cmd/oapi-codegen")
		fmt.Println(bi.Main.Version)
		return
	}

	if flag.NArg() < 1 {
		errExit("Please specify a path to a OpenAPI 3.0 spec file\n")
	} else if flag.NArg() > 1 {
		errExit("Only one OpenAPI 3.0 spec file is accepted and it must be the last CLI argument\n")
	}

	// We will try to infer whether the user has an old-style config, or a new
	// style. Start with the command line argument. If it's true, we know it's
	// old config style.
	var oldConfigStyle *bool
	if flagOldConfigStyle {
		oldConfigStyle = &flagOldConfigStyle
	}
	cfg := configFromFlags()

	// If the package name has not been specified, we will use the name of the
	// swagger file.
	if cfg.PackageName == "" {
		path := flag.Arg(0)
		baseName := filepath.Base(path)
		// Split the base name on '.' to get the first part of the file.
		nameParts := strings.Split(baseName, ".")
		cfg.PackageName = strings.ToLower(codegen.ToPascalCase(nameParts[0]))
	}

	// We don't know yet, so keep looking. Try to parse the configuration file,
	// if given.
	if oldConfigStyle == nil && (flagConfigFile != "") {
		configFile, err := os.ReadFile(flagConfigFile)
		if err != nil {
			errExit("error reading config file '%s': %v\n", flagConfigFile, err)
		}
		var oldConfig oldConfiguration
		oldErr := yaml.UnmarshalStrict(configFile, &oldConfig)

		var newConfig configuration
		newErr := yaml.UnmarshalStrict(configFile, &newConfig)

		// If one of the two files parses, but the other fails, we know the
		// answer.
		if oldErr != nil && newErr == nil {
			f := false
			oldConfigStyle = &f
		} else if oldErr == nil && newErr != nil {
			t := true
			oldConfigStyle = &t
		} else if oldErr != nil && newErr != nil {
			errExit("error parsing configuration style as old version or new version: %v\n", err)
		}
		// Else we fall through, and we still don't know, so we need to infer it from flags.
	}

	if oldConfigStyle == nil {
		// If any deprecated flag is present, and config file structure is unknown,
		// the presence of the deprecated flag means we must be using the old
		// config style. It should work correctly if we go down the old path,
		// even if we have a simple config file readable as both types.
		deprecatedFlagNames := map[string]bool{
			"include-tags":         true,
			"exclude-tags":         true,
			"import-mapping":       true,
			"exclude-schemas":      true,
			"response-type-suffix": true,
			"alias-types":          true,
		}
		hasDeprecatedFlag := false
		flag.Visit(func(f *flag.Flag) {
			if deprecatedFlagNames[f.Name] {
				hasDeprecatedFlag = true
			}
		})
		if hasDeprecatedFlag {
			t := true
			oldConfigStyle = &t
		} else {
			f := false
			oldConfigStyle = &f
		}
	}

	var opts configuration
	if !*oldConfigStyle {
		// We simply read the configuration from disk.
		if flagConfigFile != "" {
			buf, err := os.ReadFile(flagConfigFile)
			if err != nil {
				errExit("error reading config file '%s': %v\n", flagConfigFile, err)
			}
			err = yaml.Unmarshal(buf, &opts)
			if err != nil {
				errExit("error parsing'%s' as YAML: %v\n", flagConfigFile, err)
			}
		} else {
			// In the case where no config file is provided, we assume some
			// defaults, so that when this is invoked very simply, it's similar
			// to old behavior.
			opts = configuration{
				Configuration: codegen.Configuration{
					Generate: codegen.GenerateOptions{
						EchoServer:   true,
						Client:       true,
						Models:       true,
						EmbeddedSpec: true,
					},
				},
				OutputFile: flagOutputFile,
			}
		}
	}

	swagger, err := util.LoadSwagger(flag.Arg(0))
	if err != nil {
		errExit("error loading swagger spec in %s\n: %s", flag.Arg(0), err)
	}

	templates, err := loadTemplateOverrides(cfg.OutputOptions.UserTemplates["default"])
	if err != nil {
		errExit("error loading template overrides: %s\n", err)
	}
	opts.OutputOptions.UserTemplates = templates

	opts.ImportMapping = cfg.ImportMapping

	if _, err := codegen.Generate(swagger, cfg.PackageName, opts.Configuration); err != nil {
		errExit("error generating code: %s\n", err)
	}
}

func loadTemplateOverrides(templatesDir string) (map[string]string, error) {
	var templates = make(map[string]string)

	if templatesDir == "" {
		return templates, nil
	}

	files, err := os.ReadDir(templatesDir)
	if err != nil {
		return nil, err
	}

	for _, f := range files {
		// Recursively load subdirectory files, using the path relative to the templates
		// directory as the key. This allows for overriding the files in the service-specific
		// directories (e.g. echo, chi, etc.).
		if f.IsDir() {
			subFiles, err := loadTemplateOverrides(path.Join(templatesDir, f.Name()))
			if err != nil {
				return nil, err
			}
			for subDir, subFile := range subFiles {
				templates[path.Join(f.Name(), subDir)] = subFile
			}
			continue
		}
		data, err := os.ReadFile(path.Join(templatesDir, f.Name()))
		if err != nil {
			return nil, err
		}
		templates[f.Name()] = string(data)
	}

	return templates, nil
}

// detectPackageName detects and sets PackageName if not already set.
func detectPackageName(cfg *configuration) error {
	if cfg.PackageName != "" {
		return nil
	}

	if cfg.OutputFile != "" {
		// Determine from the package name of the output file.
		dir := filepath.Dir(cfg.PackageName)
		cmd := exec.Command("go", "list", "-f", "{{.Name}}", dir)
		out, err := cmd.CombinedOutput()
		if err != nil {
			outStr := string(out)
			switch {
			case strings.Contains(outStr, "expected 'package', found 'EOF'"):
				// Redirecting the output to current directory which hasn't
				// written anything yet, ignore.
			case strings.HasPrefix(outStr, "no Go files in"):
				// No go files yet, ignore.
			default:
				// Unexpected failure report.
				return fmt.Errorf("detect package name for %q output: %q: %w", dir, string(out), err)
			}
		} else {
			cfg.PackageName = string(out)
			return nil
		}
	}

	// Fallback to determining from the spec file name.
	parts := strings.Split(filepath.Base(flag.Arg(0)), ".")
	cfg.PackageName = codegen.LowercaseFirstCharacter(codegen.ToCamelCase(parts[0]))

	return nil
}

// updateConfigFromFlags updates a loaded configuration from flags. Flags
// override anything in the file. We generate errors for any unsupported
// command line flags.
func updateConfigFromFlags(cfg *configuration) error {
	if flagPackageName != "" {
		cfg.PackageName = flagPackageName
	}

	if flagGenerate != "types,client,server,spec" {
		// Override generation and output options from generate command line flag.
		if err := generationTargets(&cfg.Configuration, util.ParseCommandLineList(flagGenerate)); err != nil {
			return err
		}
	}
	if flagIncludeTags != "" {
		cfg.OutputOptions.IncludeTags = util.ParseCommandLineList(flagIncludeTags)
	}
	if flagExcludeTags != "" {
		cfg.OutputOptions.ExcludeTags = util.ParseCommandLineList(flagExcludeTags)
	}
	if flagTemplatesDir != "" {
		templates, err := loadTemplateOverrides(flagTemplatesDir)
		if err != nil {
			return fmt.Errorf("load templates from %q: %w", flagTemplatesDir, err)
		}
		cfg.OutputOptions.UserTemplates = templates
	}
	if flagImportMapping != "" {
		var err error
		cfg.ImportMapping, err = util.ParseCommandlineMap(flagImportMapping)
		if err != nil {
			return err
		}
	}
	if flagExcludeSchemas != "" {
		cfg.OutputOptions.ExcludeSchemas = util.ParseCommandLineList(flagExcludeSchemas)
	}
	if flagResponseTypeSuffix != "" {
		cfg.OutputOptions.ResponseTypeSuffix = flagResponseTypeSuffix
	}
	if flagAliasTypes {
		return fmt.Errorf("--alias-types isn't supported any more")
	}

	if cfg.OutputFile == "" {
		cfg.OutputFile = flagOutputFile
	}

	return nil
}

// updateOldConfigFromFlags parses the flags and the config file. Anything which is
// a zerovalue in the configuration file will be replaced with the flag
// value, this means that the config file overrides flag values.
func updateOldConfigFromFlags(cfg oldConfiguration) oldConfiguration {
	if cfg.PackageName == "" {
		cfg.PackageName = flagPackageName
	}
	if cfg.GenerateTargets == nil {
		cfg.GenerateTargets = util.ParseCommandLineList(flagGenerate)
	}
	if cfg.IncludeTags == nil {
		cfg.IncludeTags = util.ParseCommandLineList(flagIncludeTags)
	}
	if cfg.ExcludeTags == nil {
		cfg.ExcludeTags = util.ParseCommandLineList(flagExcludeTags)
	}
	if cfg.TemplatesDir == "" {
		cfg.TemplatesDir = flagTemplatesDir
	}
	if cfg.ImportMapping == nil && flagImportMapping != "" {
		var err error
		cfg.ImportMapping, err = util.ParseCommandlineMap(flagImportMapping)
		if err != nil {
			errExit("error parsing import-mapping: %s\n", err)
		}
	}
	if cfg.ExcludeSchemas == nil {
		cfg.ExcludeSchemas = util.ParseCommandLineList(flagExcludeSchemas)
	}
	if cfg.OutputFile == "" {
		cfg.OutputFile = flagOutputFile
	}
	return cfg
}

// generationTargets sets cfg options based on the generation targets.
func generationTargets(cfg *codegen.Configuration, targets []string) error {
	opts := codegen.GenerateOptions{} // Blank to start with.
	for _, opt := range targets {
		switch opt {
		case "chi-server", "chi":
			opts.ChiServer = true
		case "server", "echo-server", "echo":
			opts.EchoServer = true
		case "gin", "gin-server":
			opts.GinServer = true
		case "gorilla", "gorilla-server":
			opts.GorillaServer = true
		case "kit":
			opts.KitServer = true
			opts.KitServiceStub = true
			opts.KitClient = true
		case "kit-server":
			opts.KitServer = true
		case "kit-service-stub":
			opts.KitServiceStub = true
		case "kit-client":
			opts.KitClient = true
		case "strict-server":
			opts.Strict = true
		case "client":
			opts.Client = true
		case "types", "models":
			opts.Models = true
		case "spec", "embedded-spec":
			opts.EmbeddedSpec = true
		case "skip-fmt":
			cfg.OutputOptions.SkipFmt = true
		case "skip-prune":
			cfg.OutputOptions.SkipPrune = true
		default:
			return fmt.Errorf("unknown generate option %q", opt)
		}
	}
	cfg.Generate = opts

	return nil
}

func newConfigFromOldConfig(c oldConfiguration) configuration {
	// Take flags into account.
	cfg := updateOldConfigFromFlags(c)

	// Now, copy over field by field, translating flags and old values as
	// necessary.
	opts := codegen.Configuration{
		PackageName: cfg.PackageName,
	}
	opts.OutputOptions.ResponseTypeSuffix = flagResponseTypeSuffix

	if err := generationTargets(&opts, cfg.GenerateTargets); err != nil {
		fmt.Println(err)
		flag.PrintDefaults()
		os.Exit(1)
	}

	opts.OutputOptions.IncludeTags = cfg.IncludeTags
	opts.OutputOptions.ExcludeTags = cfg.ExcludeTags
	opts.OutputOptions.ExcludeSchemas = cfg.ExcludeSchemas

	templates, err := loadTemplateOverrides(cfg.TemplatesDir)
	if err != nil {
		errExit("error loading template overrides: %s\n", err)
	}
	opts.OutputOptions.UserTemplates = templates

	opts.ImportMapping = cfg.ImportMapping

	opts.Compatibility = cfg.Compatibility

	return configuration{
		Configuration: opts,
		OutputFile:    cfg.OutputFile,
	}
}

// configFromFlags parses the flags and the config file. Anything which is
// a zerovalue in the configuration file will be replaced with the flag
// value, this means that the config file overrides flag values.
func configFromFlags() *configuration {
	var cfg configuration

	// Load the configuration file first.
	if flagConfigFile != "" {
		f, err := os.Open(flagConfigFile)
		if err != nil {
			errExit("failed to open config file with error: %v\n", err)
		}
		defer f.Close()
		err = yaml.NewDecoder(f).Decode(&cfg)
		if err != nil {
			errExit("error parsing config file: %v\n", err)
		}
	}

	if cfg.PackageName == "" {
		cfg.PackageName = flagPackageName
	}

	cfg.Generate.FromCommandLine(util.ParseCommandLineList(flagGenerate))

	if cfg.OutputOptions.IncludeTags == nil {
		cfg.OutputOptions.IncludeTags = util.ParseCommandLineList(flagIncludeTags)
	}
	if cfg.OutputOptions.ExcludeTags == nil {
		cfg.OutputOptions.ExcludeTags = util.ParseCommandLineList(flagExcludeTags)
	}
	if cfg.OutputOptions.UserTemplates == nil {
		cfg.OutputOptions.UserTemplates = map[string]string{"default": flagTemplatesDir}
	}
	if cfg.ImportMapping == nil && flagImportMapping != "" {
		var err error
		cfg.ImportMapping, err = util.ParseCommandlineMap(flagImportMapping)
		if err != nil {
			errExit("error parsing import-mapping: %s\n", err)
		}
	}
	if cfg.OutputOptions.ExcludeSchemas == nil {
		cfg.OutputOptions.ExcludeSchemas = util.ParseCommandLineList(flagExcludeSchemas)
	}
	if cfg.OutputFile == "" {
		cfg.OutputFile = flagOutputFile
	}

	return &cfg
}
