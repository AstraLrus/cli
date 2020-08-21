package download

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/cli/cli/api"
	"github.com/cli/cli/internal/ghrepo"
	"github.com/cli/cli/pkg/cmd/release/shared"
	"github.com/cli/cli/pkg/cmdutil"
	"github.com/cli/cli/pkg/iostreams"
	"github.com/spf13/cobra"
)

type DownloadOptions struct {
	HttpClient func() (*http.Client, error)
	IO         *iostreams.IOStreams
	BaseRepo   func() (ghrepo.Interface, error)

	TagName     string
	FilePattern string
	Destination string

	// maximum number of simultaneous downloads
	Concurrency int
}

func NewCmdDownload(f *cmdutil.Factory, runF func(*DownloadOptions) error) *cobra.Command {
	opts := &DownloadOptions{
		IO:         f.IOStreams,
		HttpClient: f.HttpClient,
	}

	cmd := &cobra.Command{
		Use:   "download <tag> [<pattern>]",
		Short: "Download release assets",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// support `-R, --repo` override
			opts.BaseRepo = f.BaseRepo

			opts.TagName = args[0]
			if len(args) > 1 {
				opts.FilePattern = args[1]
			}

			opts.Concurrency = 5

			if runF != nil {
				return runF(opts)
			}
			return downloadRun(opts)
		},
	}

	cmd.Flags().StringVarP(&opts.Destination, "destination", "C", ".", "The directory to download files into")

	return cmd
}

func downloadRun(opts *DownloadOptions) error {
	httpClient, err := opts.HttpClient()
	if err != nil {
		return err
	}

	baseRepo, err := opts.BaseRepo()
	if err != nil {
		return err
	}

	release, err := shared.FetchRelease(httpClient, baseRepo, opts.TagName)
	if err != nil {
		return err
	}

	var toDownload []shared.ReleaseAsset
	for _, a := range release.Assets {
		if opts.FilePattern != "" {
			if isMatch, err := filepath.Match(opts.FilePattern, a.Name); err != nil || !isMatch {
				continue
			}
		}
		toDownload = append(toDownload, a)
	}

	if opts.Destination != "." {
		err := os.MkdirAll(opts.Destination, 0755)
		if err != nil {
			return err
		}
	}

	opts.IO.StartProgressIndicator()
	err = downloadAssets(httpClient, toDownload, opts.Destination, opts.Concurrency)
	opts.IO.StopProgressIndicator()
	return err
}

func downloadAssets(httpClient *http.Client, toDownload []shared.ReleaseAsset, destDir string, numWorkers int) error {
	if numWorkers == 0 {
		return errors.New("the number of concurrent workers needs to be greater than 0")
	}

	jobs := make(chan shared.ReleaseAsset, len(toDownload))
	results := make(chan error, len(toDownload))

	for w := 1; w <= numWorkers; w++ {
		go func() {
			for a := range jobs {
				results <- downloadAsset(httpClient, a.URL, filepath.Join(destDir, a.Name))
			}
		}()
	}

	for _, a := range toDownload {
		jobs <- a
	}
	close(jobs)

	var downloadError error
	for i := 0; i < len(toDownload); i++ {
		if err := <-results; err != nil {
			downloadError = err
		}
	}

	return downloadError
}

func downloadAsset(httpClient *http.Client, assetURL, destinationPath string) error {
	req, err := http.NewRequest("GET", assetURL, nil)
	if err != nil {
		return err
	}

	req.Header.Set("Accept", "application/octet-stream")

	resp, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode > 299 {
		return api.HandleHTTPError(resp)
	}

	f, err := os.OpenFile(destinationPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	_, err = io.Copy(f, resp.Body)
	return err
}