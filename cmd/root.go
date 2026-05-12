package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

var (
	projectFlag string
	outputFlag  string
)

var rootCmd = &cobra.Command{
	Use:   "techgo",
	Short: "Static site generator for the Tech.Go podcast",
	Long: `techgo builds the Tech.Go podcast website from per-episode YAML data files
and source media, transcodes audio/video with ffmpeg, generates a podcast RSS
feed, produces the favicon/cover image set, and deploys to S3 + CloudFront.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

// Execute runs the root command. It is the single entry point called by main.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "techgo: "+err.Error())
		os.Exit(1)
	}
}

func init() {
	rootCmd.PersistentFlags().StringVarP(&projectFlag, "project", "p", ".",
		"path to the project directory (contains site.yml, data/, templates/, static/)")
	rootCmd.PersistentFlags().StringVarP(&outputFlag, "output", "o", "public",
		"path to the build output directory (relative to the project dir unless absolute)")
}

// ProjectDir returns the absolute path of the project directory selected via --project.
func ProjectDir() (string, error) {
	abs, err := filepath.Abs(projectFlag)
	if err != nil {
		return "", fmt.Errorf("resolving --project %q: %w", projectFlag, err)
	}
	return abs, nil
}

// OutputDir returns the absolute path of the build output directory selected via
// --output. A relative --output is resolved against the project directory.
func OutputDir() (string, error) {
	if filepath.IsAbs(outputFlag) {
		return filepath.Clean(outputFlag), nil
	}
	proj, err := ProjectDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(proj, outputFlag), nil
}
