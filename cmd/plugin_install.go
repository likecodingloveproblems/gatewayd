package cmd

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"github.com/codingsince1985/checksum"
	"github.com/gatewayd-io/gatewayd/config"
	"github.com/getsentry/sentry-go"
	"github.com/google/go-github/v53/github"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const (
	NumParts                    int         = 2
	LatestVersion               string      = "latest"
	FolderPermissions           os.FileMode = 0o755
	DefaultPluginConfigFilename string      = "./gatewayd_plugin.yaml"
	GitHubURLPrefix             string      = "github.com/"
	GitHubURLRegex              string      = `^github.com\/[a-zA-Z0-9\-]+\/[a-zA-Z0-9\-]+@(?:latest|v(=|>=|<=|=>|=<|>|<|!=|~|~>|\^)?(?P<major>0|[1-9]\d*)\.(?P<minor>0|[1-9]\d*)\.(?P<patch>0|[1-9]\d*)(?:-(?P<prerelease>(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*)(?:\.(?:0|[1-9]\d*|\d*[a-zA-Z-][0-9a-zA-Z-]*))*))?(?:\+(?P<buildmetadata>[0-9a-zA-Z-]+(?:\.[0-9a-zA-Z-]+)*))?)$` //nolint:lll
	ExtWindows                  string      = ".zip"
	ExtOthers                   string      = ".tar.gz"
)

var (
	pluginOutputDir string
	pullOnly        bool
)

// pluginInstallCmd represents the plugin install command.
var pluginInstallCmd = &cobra.Command{
	Use:     "install",
	Short:   "Install a plugin from a remote location",
	Example: "  gatewayd plugin install github.com/gatewayd-io/gatewayd-plugin-cache@latest",
	Run: func(cmd *cobra.Command, args []string) {
		// Enable Sentry.
		if enableSentry {
			// Initialize Sentry.
			err := sentry.Init(sentry.ClientOptions{
				Dsn:              DSN,
				TracesSampleRate: config.DefaultTraceSampleRate,
				AttachStacktrace: config.DefaultAttachStacktrace,
			})
			if err != nil {
				log.Fatal("Sentry initialization failed: ", err)
			}

			// Flush buffered events before the program terminates.
			defer sentry.Flush(config.DefaultFlushTimeout)
			// Recover from panics and report the error to Sentry.
			defer sentry.Recover()
		}

		// Validate the number of arguments.
		if len(args) < 1 {
			log.Fatal(
				"Invalid URL. Use the following format: github.com/account/repository@version")
		}

		// Validate the URL.
		validGitHubURL := regexp.MustCompile(GitHubURLRegex)
		if !validGitHubURL.MatchString(args[0]) {
			log.Fatal(
				"Invalid URL. Use the following format: github.com/account/repository@version")
		}

		// Get the plugin version.
		pluginVersion := LatestVersion
		splittedURL := strings.Split(args[0], "@")
		// If the version is not specified, use the latest version.
		if len(splittedURL) < NumParts {
			log.Println("Version not specified. Using latest version")
		}
		if len(splittedURL) >= NumParts {
			pluginVersion = splittedURL[1]
		}

		// Get the plugin account and repository.
		accountRepo := strings.Split(strings.TrimPrefix(splittedURL[0], GitHubURLPrefix), "/")
		if len(accountRepo) != NumParts {
			log.Fatal(
				"Invalid URL. Use the following format: github.com/account/repository@version")
		}
		account := accountRepo[0]
		pluginName := accountRepo[1]
		if account == "" || pluginName == "" {
			log.Fatal(
				"Invalid URL. Use the following format: github.com/account/repository@version")
		}

		// Get the release artifact from GitHub.
		client := github.NewClient(nil)
		var release *github.RepositoryRelease
		var err error
		if pluginVersion == LatestVersion || pluginVersion == "" {
			// Get the latest release.
			release, _, err = client.Repositories.GetLatestRelease(
				context.Background(), account, pluginName)
		} else if strings.HasPrefix(pluginVersion, "v") {
			// Get an specific release.
			release, _, err = client.Repositories.GetReleaseByTag(
				context.Background(), account, pluginName, pluginVersion)
		}
		if err != nil {
			log.Fatal("The plugin could not be found")
		}

		if release == nil {
			log.Fatal("The plugin could not be found")
		}

		downloadFile := func(downloadURL string, releaseID int64, filename string) {
			log.Println("Downloading", downloadURL)

			// Download the plugin.
			readCloser, redirectURL, err := client.Repositories.DownloadReleaseAsset(
				context.Background(), account, pluginName, releaseID, http.DefaultClient)
			if err != nil {
				log.Fatal("There was an error downloading the plugin: ", err)
			}

			var reader io.ReadCloser
			if readCloser != nil {
				reader = readCloser
				defer readCloser.Close()
			} else if redirectURL != "" {
				// Download the plugin from the redirect URL.
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirectURL, nil)
				if err != nil {
					log.Fatal("There was an error downloading the plugin: ", err)
				}

				resp, err := http.DefaultClient.Do(req)
				if err != nil {
					log.Fatal("There was an error downloading the plugin: ", err)
				}
				defer resp.Body.Close()

				reader = resp.Body
			}

			if reader != nil {
				defer reader.Close()
			} else {
				log.Fatal("The plugin could not be downloaded, please try again later")
			}

			// Create the output file in the current directory and write the downloaded content.
			cwd, err := os.Getwd()
			if err != nil {
				log.Fatal("There was an error downloading the plugin: ", err)
			}
			output, err := os.Create(path.Join([]string{cwd, filename}...))
			if err != nil {
				log.Fatal("There was an error downloading the plugin: ", err)
			}
			defer output.Close()

			// Write the bytes to the file.
			_, err = io.Copy(output, reader)
			if err != nil {
				log.Fatal("There was an error downloading the plugin: ", err)
			}

			log.Println("Download completed successfully")
		}

		findAsset := func(match func(string) bool) (string, string, int64) {
			// Find the matching release.
			for _, asset := range release.Assets {
				if match(asset.GetName()) {
					return asset.GetName(), asset.GetBrowserDownloadURL(), asset.GetID()
				}
			}
			return "", "", 0
		}

		// Get the archive extension.
		archiveExt := ExtOthers
		if runtime.GOOS == "windows" {
			archiveExt = ExtWindows
		}

		// Find and download the plugin binary from the release assets.
		pluginFilename, downloadURL, releaseID := findAsset(func(name string) bool {
			return strings.Contains(name, runtime.GOOS) &&
				strings.Contains(name, runtime.GOARCH) &&
				strings.Contains(name, archiveExt)
		})
		if downloadURL != "" && releaseID != 0 {
			downloadFile(downloadURL, releaseID, pluginFilename)
		} else {
			log.Fatal("The plugin file could not be found in the release assets")
		}

		// Find and download the checksums.txt from the release assets.
		checksumsFilename, downloadURL, releaseID := findAsset(func(name string) bool {
			return strings.Contains(name, "checksums.txt")
		})
		if checksumsFilename != "" && downloadURL != "" && releaseID != 0 {
			downloadFile(downloadURL, releaseID, checksumsFilename)
		} else {
			log.Fatal("The checksum file could not be found in the release assets")
		}

		// Read the checksums text file.
		checksums, err := os.ReadFile(checksumsFilename)
		if err != nil {
			log.Fatal("There was an error reading the checksums file: ", err)
		}

		// Get the checksum for the plugin binary.
		sum, err := checksum.SHA256sum(pluginFilename)
		if err != nil {
			log.Fatal("There was an error calculating the checksum: ", err)
		}

		// Verify the checksums.
		checksumLines := strings.Split(string(checksums), "\n")
		for _, line := range checksumLines {
			if strings.Contains(line, pluginFilename) {
				checksum := strings.Split(line, " ")[0]
				if checksum != sum {
					log.Fatal("Checksum verification failed")
				}

				log.Println("Checksum verification passed")
				break
			}
		}

		if pullOnly {
			log.Println("Plugin binary downloaded to", pluginFilename)
			return
		}

		// Extract the archive.
		var filenames []string
		if runtime.GOOS == "windows" {
			filenames = extractZip(pluginFilename, pluginOutputDir)
		} else {
			filenames = extractTarGz(pluginFilename, pluginOutputDir)
		}

		// Find the extracted plugin binary.
		localPath := ""
		pluginFileSum := ""
		for _, filename := range filenames {
			if strings.Contains(filename, pluginName) {
				log.Println("Plugin binary extracted to", filename)
				localPath = filename
				// Get the checksum for the extracted plugin binary.
				// TODO: Should we verify the checksum using the checksum.txt file instead?
				pluginFileSum, err = checksum.SHA256sum(filename)
				if err != nil {
					log.Fatal("There was an error calculating the checksum: ", err)
				}
				break
			}
		}

		// Remove the tar.gz file.
		err = os.Remove(pluginFilename)
		if err != nil {
			log.Fatal("There was an error removing the downloaded plugin file: ", err)
		}

		// Remove the checksums.txt file.
		err = os.Remove(checksumsFilename)
		if err != nil {
			log.Fatal("There was an error removing the checksums file: ", err)
		}

		// Create a new gatewayd_plugins.yaml file if it doesn't exist.
		if _, err := os.Stat(pluginConfigFile); os.IsNotExist(err) {
			generateConfig(cmd, Plugins, pluginConfigFile, false)
		}

		// Read the gatewayd_plugins.yaml file.
		pluginsConfig, err := os.ReadFile(pluginConfigFile)
		if err != nil {
			log.Fatal(err)
		}

		// Get the registered plugins from the plugins configuration file.
		var localPluginsConfig map[string]interface{}
		if err := yaml.Unmarshal(pluginsConfig, &localPluginsConfig); err != nil {
			log.Fatal("Failed to unmarshal the plugins configuration file: ", err)
		}
		pluginsList, ok := localPluginsConfig["plugins"].([]interface{}) //nolint:varnamelen
		if !ok {
			log.Fatal("There was an error reading the plugins file from disk")
		}

		// Get the list of files in the repository.
		var repoContents *github.RepositoryContent
		repoContents, _, _, err = client.Repositories.GetContents(
			context.Background(), account, pluginName, DefaultPluginConfigFilename, nil)
		if err != nil {
			log.Fatal("There was an error getting the default plugins configuration file: ", err)
		}
		// Get the contents of the file.
		contents, err := repoContents.GetContent()
		if err != nil {
			log.Fatal("There was an error getting the default plugins configuration file: ", err)
		}

		// Get the plugin configuration from the downloaded plugins configuration file.
		var downloadedPluginConfig map[string]interface{}
		if err := yaml.Unmarshal([]byte(contents), &downloadedPluginConfig); err != nil {
			log.Fatal("Failed to unmarshal the downloaded plugins configuration file: ", err)
		}
		defaultPluginConfig, ok := downloadedPluginConfig["plugins"].([]interface{})
		if !ok {
			log.Fatal("There was an error reading the plugins file from the repository")
		}
		// Get the plugin configuration.
		pluginConfig, ok := defaultPluginConfig[0].(map[string]interface{})
		if !ok {
			log.Fatal("There was an error reading the default plugin configuration")
		}

		// Update the plugin's local path and checksum.
		pluginConfig["localPath"] = localPath
		pluginConfig["checksum"] = pluginFileSum

		// TODO: Check if the plugin is already installed.

		// Add the plugin config to the list of plugin configs.
		pluginsList = append(pluginsList, pluginConfig)
		// Merge the result back into the config map.
		localPluginsConfig["plugins"] = pluginsList

		// Marshal the map into YAML.
		updatedPlugins, err := yaml.Marshal(localPluginsConfig)
		if err != nil {
			log.Fatal("There was an error marshalling the plugins configuration: ", err)
		}

		// Write the YAML to the plugins config file.
		if err = os.WriteFile(pluginConfigFile, updatedPlugins, FilePermissions); err != nil {
			log.Fatal("There was an error writing the plugins configuration file: ", err)
		}

		// TODO: Clean up the plugin files if the installation fails.
		// TODO: Add a rollback mechanism.
		log.Println("Plugin installed successfully")
	},
}

func extractZip(filename, dest string) []string {
	// Open and extract the zip file.
	zipRc, err := zip.OpenReader(filename)
	if err != nil {
		if zipRc != nil {
			zipRc.Close()
		}
		log.Fatal("There was an error opening the downloaded plugin file: ", err)
	}

	// Create the output directory if it doesn't exist.
	if err := os.MkdirAll(dest, FolderPermissions); err != nil {
		log.Fatal("Failed to create directories: ", err)
	}

	// Extract the files.
	filenames := []string{}
	for _, file := range zipRc.File {
		switch fileInfo := file.FileInfo(); {
		case fileInfo.IsDir():
			// Sanitize the path.
			filename := filepath.Clean(file.Name)
			if !path.IsAbs(filename) {
				destPath := path.Join(dest, filename)
				// Create the directory.

				if err := os.MkdirAll(destPath, FolderPermissions); err != nil {
					log.Fatal("Failed to create directories: ", err)
				}
			}
		case fileInfo.Mode().IsRegular():
			// Sanitize the path.
			outFilename := filepath.Join(filepath.Clean(dest), filepath.Clean(file.Name))

			// Check for ZipSlip.
			if strings.HasPrefix(outFilename, string(os.PathSeparator)) {
				log.Fatal("Invalid file path in zip archive, aborting")
			}

			// Create the file.
			outFile, err := os.Create(outFilename)
			if err != nil {
				log.Fatal("Failed to create file: ", err)
			}

			// Open the file in the zip archive.
			fileRc, err := file.Open()
			if err != nil {
				log.Fatal("Failed to open file in zip archive: ", err)
			}

			// Copy the file contents.
			if _, err := io.Copy(outFile, io.LimitReader(fileRc, MaxFileSize)); err != nil {
				outFile.Close()
				os.Remove(outFilename)
				log.Fatal("Failed to write to the file: ", err)
			}
			outFile.Close()

			fileMode := file.FileInfo().Mode()
			// Set the file permissions.
			if fileMode.IsRegular() && fileMode&ExecFileMask != 0 {
				if err := os.Chmod(outFilename, ExecFilePermissions); err != nil {
					log.Fatal("Failed to set executable file permissions: ", err)
				}
			} else {
				if err := os.Chmod(outFilename, FilePermissions); err != nil {
					log.Fatal("Failed to set file permissions: ", err)
				}
			}

			filenames = append(filenames, outFile.Name())
		default:
			log.Fatalf("Failed to extract zip archive: unknown type: %s", file.Name)
		}
	}

	if zipRc != nil {
		zipRc.Close()
	}

	return filenames
}

func extractTarGz(filename, dest string) []string {
	// Open and extract the tar.gz file.
	gzipStream, err := os.Open(filename)
	if err != nil {
		log.Fatal("There was an error opening the downloaded plugin file: ", err)
	}

	uncompressedStream, err := gzip.NewReader(gzipStream)
	if err != nil {
		if gzipStream != nil {
			gzipStream.Close()
		}
		log.Fatal("Failed to extract tarball: ", err)
	}

	// Create the output directory if it doesn't exist.
	if err := os.MkdirAll(dest, FolderPermissions); err != nil {
		log.Fatal("Failed to create directories: ", err)
	}

	tarReader := tar.NewReader(uncompressedStream)
	filenames := []string{}

	for {
		header, err := tarReader.Next()

		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			log.Fatal("Failed to extract tarball: ", err)
		}

		switch header.Typeflag {
		case tar.TypeDir:
			// Sanitize the path
			cleanPath := filepath.Clean(header.Name)
			// Ensure it is not an absolute path
			if !path.IsAbs(cleanPath) {
				destPath := path.Join(dest, cleanPath)
				if err := os.MkdirAll(destPath, FolderPermissions); err != nil {
					log.Fatal("Failed to create directories: ", err)
				}
			}
		case tar.TypeReg:
			// Sanitize the path
			outFilename := path.Join(filepath.Clean(dest), filepath.Clean(header.Name))

			// Check for TarSlip.
			if strings.HasPrefix(outFilename, string(os.PathSeparator)) {
				log.Fatal("Invalid file path in tarball, aborting")
			}

			// Create the file.
			outFile, err := os.Create(outFilename)
			if err != nil {
				log.Fatal("Failed to create file: ", err)
			}
			if _, err := io.Copy(outFile, io.LimitReader(tarReader, MaxFileSize)); err != nil {
				outFile.Close()
				os.Remove(outFilename)
				log.Fatal("Failed to write to the file: ", err)
			}
			outFile.Close()

			fileMode := header.FileInfo().Mode()
			// Set the file permissions
			if fileMode.IsRegular() && fileMode&ExecFileMask != 0 {
				if err := os.Chmod(outFilename, ExecFilePermissions); err != nil {
					log.Fatal("Failed to set executable file permissions: ", err)
				}
			} else {
				if err := os.Chmod(outFilename, FilePermissions); err != nil {
					log.Fatal("Failed to set file permissions: ", err)
				}
			}

			filenames = append(filenames, outFile.Name())
		default:
			log.Fatalf(
				"Failed to extract tarball: unknown type: %s in %s",
				string(header.Typeflag),
				header.Name)
		}
	}

	if gzipStream != nil {
		gzipStream.Close()
	}

	return filenames
}

func init() {
	pluginCmd.AddCommand(pluginInstallCmd)

	pluginInstallCmd.Flags().StringVarP(
		&pluginConfigFile, // Already exists in run.go
		"plugin-config", "p", config.GetDefaultConfigFilePath(config.PluginsConfigFilename),
		"Plugin config file")
	pluginInstallCmd.Flags().StringVarP(
		&pluginOutputDir, "output-dir", "o", "./plugins", "Output directory for the plugin")
	pluginInstallCmd.Flags().BoolVar(
		&pullOnly, "pull-only", false, "Only pull the plugin, don't install it")
	pluginInstallCmd.Flags().BoolVar(
		&enableSentry, "sentry", true, "Enable Sentry") // Already exists in run.go
}
