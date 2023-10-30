package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ipfs/boxo/coreiface/path"
	"github.com/ipfs/kubo/client/rpc"
	"github.com/multiformats/go-multiaddr"
)

func main() {
	apiAddressStr := flag.String("api-address", "", "address of the IPFS API")
	email := flag.String("email", "", "Email address for your IPFS Podcasting account")
	updateFrequency := flag.Duration(
		"update-frequency",
		10*time.Minute,
		"How often to check for new work",
	)
	httpTimeout := flag.Duration(
		"http-timeout",
		5*time.Minute,
		"Timeout for HTTP requests. For downloading epodes and communicating with Kubo",
	)
	flag.Parse()

	if *apiAddressStr == "" {
		slog.Error("api-address missing. This flag is required.")
		os.Exit(2)
	}

	if *email == "" {
		slog.Error("email missing. This flag is required. Set to email@example.com if you don't want to set it.")
		os.Exit(2)
	}

	slog.Info("starting", "api-address", *apiAddressStr, "email", *email)

	apiAddress, err := multiaddr.NewMultiaddr(*apiAddressStr)
	if err != nil {
		slog.Error("parsing api-address failed", "err", err)
		os.Exit(1)
	}

	httpClient := &http.Client{
		Timeout: *httpTimeout,
	}

	client, err := rpc.NewApiWithClient(apiAddress, httpClient)
	if err != nil {
		slog.Error("creating api client failed", "err", err)
		os.Exit(1)
	}

	workRequest := WorkResponse{
		Email:   *email,
		Version: "0.6p",
	}

	for {
		nextUpdate := time.Now().Add(*updateFrequency)

		completed, err := doWork(client, httpClient, workRequest)
		if err != nil {
			slog.Error("job failed", "err", err)
		}

		if completed {
			time.Sleep(5 * time.Second)
			continue
		}

		time.Sleep(time.Until(nextUpdate))
	}
}

// first return value is if the operation was complete, or false if it exited early for any reason
func doWork(client *rpc.HttpApi, httpClient *http.Client, workResponse WorkResponse) (bool, error) {
	errInt := 1

	nID, err := nodeID(client)
	if err != nil {
		return false, fmt.Errorf("getting node id failed: %w", err)
	}

	workResponse.IPFSID = nID.ID

	sys, err := diagSys(client)
	if err != nil {
		return false, fmt.Errorf("getting diag/sys failed: %w", err)
	}

	workResponse.IPFSVersion = sys.IPFSVersion
	workResponse.Online = sys.Net.Online

	peers, err := getPeers(client)
	if err != nil {
		return false, fmt.Errorf("fetching peers failed: %w", err)
	}

	workResponse.Peers = peers

	work, err := requestWork(httpClient, workResponse)
	if err != nil {
		return false, fmt.Errorf("requesting work failed: %w", err)
	}

	if work.Message == "No Work" {
		return false, nil
	}

	if work.Download != "" && work.Filename != "" {
		slog.Info("Got download job", "download", work.Download, "filename", work.Filename)

		downloaded, err := downloadFile(client, httpClient, work.Download, work.Filename)
		if err != nil {
			slog.Error("downloading file failed", "file", work.Download, "err", err)
			workResponse.Error = &errInt
		} else {
			workResponse.Downloaded = &downloaded.DownloadedFile
			workResponse.Length = &downloaded.Length
		}
	}

	if work.Pin != "" {
		slog.Info("Got pin job", "pin", work.Pin)

		err := pinAdd(client, work.Pin)
		if err != nil {
			slog.Error("pin add failed", "err", err)
			workResponse.Error = &errInt
		} else {
			lsResp, err := ls(client, work.Pin)
			if err != nil {
				slog.Error("ls failed", "err", err)
				workResponse.Error = &errInt
			} else {
				if len(lsResp.Objects) != 1 && len(lsResp.Objects[0].Links) != 1 {
					slog.Error("ls objects or links is not 1", "ls", lsResp)
					workResponse.Error = &errInt
				} else {
					link := lsResp.Objects[0].Links[0]
					pinned := link.Hash + "/" + work.Pin
					workResponse.Pinned = &pinned
					workResponse.Length = &link.Size
				}
			}
		}
	}

	if work.Delete != "" {
		slog.Info("Got delete job", "delete", work.Delete)

		err := pinDelete(client, work.Delete)
		if err != nil {
			slog.Error("pin delete failed", "err", err)
			workResponse.Error = &errInt
		} else {
			workResponse.Deleted = &work.Delete
		}
	}

	stats, err := repoStats(client)
	if err != nil {
		slog.Error("repo stat failed", "err", err)
	} else {
		workResponse.Avail = &stats.StorageMax
		workResponse.Used = &stats.RepoSize
	}

	err = responseWork(httpClient, workResponse)
	if err != nil {
		return false, fmt.Errorf("post stats failed: %w", err)
	}

	return true, nil
}

type repoStatsResponse struct {
	RepoSize   int    `json:"RepoSize"`
	StorageMax int    `json:"StorageMax"`
	NumObjects int    `json:"NumObjects"`
	RepoPath   string `json:"RepoPath"`
	Version    string `json:"Version"`
}

func repoStats(client *rpc.HttpApi) (*repoStatsResponse, error) {
	resp, err := client.Request("repo/stat").Send(context.Background())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("response failed: %s", resp.Error.Message)
	}
	defer resp.Output.Close()

	decoder := json.NewDecoder(resp.Output)
	stats := new(repoStatsResponse)

	err = decoder.Decode(stats)
	if err != nil {
		return nil, fmt.Errorf("decoding json failed: %w", err)
	}

	return stats, nil
}

func pinDelete(client *rpc.HttpApi, hash string) error {
	err := client.Pin().Rm(context.Background(), path.New(hash))
	if err != nil {
		// This error is OK for us. Sometimes we get delete requests for
		// files we don't have pinned. That's OK.
		if strings.Contains(err.Error(), "not pinned or pinned indirectly") {
			return nil
		}
		return fmt.Errorf("request failed: %w", err)
	}

	return nil
}

func pinAdd(client *rpc.HttpApi, hash string) error {
	err := client.Pin().Add(context.Background(), path.New(hash))
	if err != nil {
		return fmt.Errorf("request failed: %w", err)
	}

	return nil
}

type lsResponse struct {
	Objects []struct {
		Hash  string `json:"Hash"`
		Links []struct {
			Name   string `json:"Name"`
			Hash   string `json:"Hash"`
			Size   int    `json:"Size"`
			Type   int    `json:"Type"`
			Target string `json:"Target"`
		} `json:"links"`
	} `json:"Objects"`
}

func ls(client *rpc.HttpApi, hash string) (*lsResponse, error) {
	resp, err := client.Request("ls", hash).Send(context.Background())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("response failed: %s", resp.Error.Message)
	}
	defer resp.Output.Close()

	decoder := json.NewDecoder(resp.Output)
	ls := new(lsResponse)

	err = decoder.Decode(ls)
	if err != nil {
		return nil, fmt.Errorf("json decode failed: %w", err)
	}

	return ls, nil
}

func fileSize(client *rpc.HttpApi, hash string) (int, error) {
	lsResp, err := ls(client, hash)
	if err != nil {
		return 0, fmt.Errorf("ls failed: %w", err)
	}

	total := 0
	for _, object := range lsResp.Objects {
		for _, link := range object.Links {
			total += link.Size
		}
	}

	return total, nil
}

type addResponse struct {
	Name string `json:"Name"`
	Hash string `json:"Hash"`
	Size int    `json:"Size,string"`
}

type downloadFileResponse struct {
	DownloadedFile string
	Length         int
}

func downloadFile(client *rpc.HttpApi, httpClient *http.Client, download string, filename string) (*downloadFileResponse, error) {
	downloadResp, err := httpClient.Get(download)
	if err != nil {
		return nil, fmt.Errorf("download failed: %w", err)
	}
	defer downloadResp.Body.Close()

	body, writer := io.Pipe()
	reqMultipart := multipart.NewWriter(writer)

	req := client.Request("add")
	req = req.Option("wrap-with-directory", true)
	req.Header("Content-Type", reqMultipart.FormDataContentType())
	req.Body(body)

	var mpwCreateFormFileErr, copyErr, mpwCloseErr error

	go func() {
		w, err := reqMultipart.CreateFormFile("file", filename)
		if err != nil {
			mpwCreateFormFileErr = err
			return
		}

		_, copyErr = io.Copy(w, downloadResp.Body)
		if err != nil {
			return
		}

		mpwCloseErr = reqMultipart.Close()
	}()

	resp, err := req.Send(context.Background())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("response failed: %s", resp.Error.Message)
	}
	defer resp.Output.Close()

	if mpwCreateFormFileErr != nil {
		return nil, fmt.Errorf("creating form file failed: %w", err)
	}
	if copyErr != nil {
		return nil, fmt.Errorf("copy download failed: %w", err)
	}
	if mpwCloseErr != nil {
		return nil, fmt.Errorf("closing mutlipart writer failed: %w", err)
	}

	decoder := json.NewDecoder(resp.Output)

	added := [2]addResponse{}

	err = decoder.Decode(&added[0])
	if err != nil {
		return nil, fmt.Errorf("json decode failed: %w", err)
	}

	err = decoder.Decode(&added[1])
	if err != nil {
		return nil, fmt.Errorf("json decode failed: %w", err)
	}

	size, err := fileSize(client, added[0].Hash)
	if err != nil {
		return nil, fmt.Errorf("getting file size failed: %w", err)
	}

	return &downloadFileResponse{
		DownloadedFile: added[0].Hash + "/" + added[1].Hash,
		Length:         size,
	}, nil
}

func getPeers(client *rpc.HttpApi) (int, error) {
	connectionInfo, err := client.Swarm().Peers(context.Background())
	if err != nil {
		return 0, fmt.Errorf("requesting peers failed: %w", err)
	}

	return len(connectionInfo), nil
}

//	{
//	  "diskinfo": {
//	    "free_space": 45147315712,
//	    "fstype": "3393526350",
//	    "total_space": 44452741120
//	  },
//	  "environment": {
//	    "GOPATH": "",
//	    "IPFS_PATH": ""
//	  },
//	  "ipfs_commit": "",
//	  "ipfs_version": "0.23.0",
//	  "memory": {
//	    "swap": 0,
//	    "virt": 2983384000
//	  },
//	  "net": {
//	    "interface_addresses": [
//	      "/ip4/127.0.0.1",
//	      "/ip4/192.168.0.160",
//	      "/ip4/192.168.122.1",
//	      "/ip4/100.89.52.31",
//	      "/ip4/172.18.0.1",
//	      "/ip4/172.17.0.1",
//	      "/ip6/::1",
//	      "/ip6/fe80::f2eb:eebb:44f5:837a",
//	      "/ip6/fd7a:115c:a1e0:ab12:4843:cd96:6259:341f",
//	      "/ip6/fe80::49b2:7ef3:ee2:ca18"
//	    ],
//	    "online": true
//	  },
//	  "runtime": {
//	    "arch": "amd64",
//	    "compiler": "gc",
//	    "gomaxprocs": 16,
//	    "numcpu": 16,
//	    "numgoroutines": 283,
//	    "os": "linux",
//	    "version": "go1.21.3"
//	  }
//	}
type DiagSysResponse struct {
	DiskInfo struct {
		FreeSpace  int64  `json:"free_space"`
		FSType     string `json:"fstype"`
		TotalSpace int64  `json:"total_space"`
	} `json:"diskinfo"`
	Environment struct {
		GoPath   string `json:"GOPATH"`
		IPFSPath string `json:"IPFS_PATH"`
	} `json:"environment"`
	IPFSCommit  string `json:"ipfs_commit"`
	IPFSVersion string `json:"ipfs_version"`
	Memory      struct {
		Swap int64 `json:"swap"`
		Virt int64 `json:"virt"`
	} `json:"memory"`
	Net struct {
		InterfaceAddresses []string `json:"interface_addresses"`
		Online             bool     `json:"online"`
	} `json:"net"`
	Runtime struct {
		Arch          string `json:"arch"`
		Compiler      string `json:"compiler"`
		GoMacProcs    int    `json:"gomaxprocs"`
		NumCPUs       int    `json:"numcpu"`
		NumGoroutines int    `json:"numgoroutines"`
		OS            string `json:"os"`
		Version       string `json:"version"`
	}
}

//	{
//	  "ID": "12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	  "PublicKey": "CAESIJiZuBDyMqYaXmHzPgbKoOKHhKhPAgFkU/xt0563KZ81",
//	  "Addresses": [
//	    "/ip4/127.0.0.1/tcp/4001/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/127.0.0.1/udp/4001/quic-v1/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/127.0.0.1/udp/4001/quic-v1/webtransport/certhash/uEiCL4zOsXA211I8dPzeQTR7Ws8CyRhyNUI0trGwOR5a-JA/certhash/uEiAPDBPZGNogGfelJLdGoNDIe3iVUZCpX-llOfV6JI7ehw/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/144.202.116.156/tcp/4001/p2p/12D3KooWMeJti8EyULiL6Ae1SaHN8uhhgjZWpkuT2Rak6vSHfhcj/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",    "/ip4/144.202.116.156/udp/4001/quic-v1/p2p/12D3KooWMeJti8EyULiL6Ae1SaHN8uhhgjZWpkuT2Rak6vSHfhcj/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/144.202.116.156/udp/4001/quic/p2p/12D3KooWMeJti8EyULiL6Ae1SaHN8uhhgjZWpkuT2Rak6vSHfhcj/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/192.168.0.160/tcp/4001/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/192.168.0.160/udp/4001/quic-v1/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/192.168.0.160/udp/4001/quic-v1/webtransport/certhash/uEiCL4zOsXA211I8dPzeQTR7Ws8CyRhyNUI0trGwOR5a-JA/certhash/uEiAPDBPZGNogGfelJLdGoNDIe3iVUZCpX-llOfV6JI7ehw/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/64.20.50.242/tcp/4001/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/64.20.50.242/udp/4001/quic-v1/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip4/64.20.50.242/udp/4001/quic-v1/webtransport/certhash/uEiDaxiUKVD_6DcKDiWcumyWrtIkIXT2rNlo0k8EgpyT0Og/certhash/uEiArSVE3Q14fQzk2NU8CtG_xATGO1XrzTRWBglw5IbNKxg/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/2604:a00:50:b9:aaa1:59ff:fec7:2082/tcp/4001/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/2604:a00:50:b9:aaa1:59ff:fec7:2082/udp/4001/quic-v1/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/2604:a00:50:b9:aaa1:59ff:fec7:2082/udp/4001/quic-v1/webtransport/certhash/uEiDaxiUKVD_6DcKDiWcumyWrtIkIXT2rNlo0k8EgpyT0Og/certhash/uEiArSVE3Q14fQzk2NU8CtG_xATGO1XrzTRWBglw5IbNKxg/p2p/12D3KooWFCxURh5KFQrP4YwxG9aPbMQjrBrm7HBMdFCW9feWoRyh/p2p-circuit/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/::1/tcp/4001/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/::1/udp/4001/quic-v1/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg",
//	    "/ip6/::1/udp/4001/quic-v1/webtransport/certhash/uEiCL4zOsXA211I8dPzeQTR7Ws8CyRhyNUI0trGwOR5a-JA/certhash/uEiAPDBPZGNogGfelJLdGoNDIe3iVUZCpX-llOfV6JI7ehw/p2p/12D3KooWL6466mzdYUHCBRabjfAZTL5BbzVGCsgfRnH8NhbejiSg"
//	  ],
//	  "AgentVersion": "kubo/0.23.0/",
//	  "Protocols": [
//	    "/ipfs/bitswap",
//	    "/ipfs/bitswap/1.0.0",
//	    "/ipfs/bitswap/1.1.0",
//	    "/ipfs/bitswap/1.2.0",
//	    "/ipfs/id/1.0.0",
//	    "/ipfs/id/push/1.0.0",
//	    "/ipfs/lan/kad/1.0.0",
//	    "/ipfs/ping/1.0.0",
//	    "/libp2p/circuit/relay/0.2.0/stop",
//	    "/x/"
//	  ]
//	}
type IDResponse struct {
	ID           string   `json:"ID"`
	PublicKey    string   `json:"PublicKey"`
	Addresses    []string `json:"Addresses"`
	AgentVersion string   `json:"AgentVersion"`
	Protocols    []string `json:"Protocols"`
}

func nodeID(client *rpc.HttpApi) (*IDResponse, error) {
	resp, err := client.Request("id").Send(context.Background())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("response error: %s", resp.Error.Message)
	}

	decoder := json.NewDecoder(resp.Output)
	idResp := new(IDResponse)

	err = decoder.Decode(idResp)
	if err != nil {
		return nil, fmt.Errorf("decoding diag/sys response failed: %w", err)
	}

	return idResp, nil
}

func diagSys(client *rpc.HttpApi) (*DiagSysResponse, error) {
	resp, err := client.Request("diag/sys").Send(context.Background())
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	if resp.Error != nil {
		return nil, fmt.Errorf("response error: %s", resp.Error.Message)
	}

	decoder := json.NewDecoder(resp.Output)
	diagSysResp := new(DiagSysResponse)

	err = decoder.Decode(diagSysResp)
	if err != nil {
		return nil, fmt.Errorf("decoding diag/sys response failed: %w", err)
	}

	return diagSysResp, nil
}

type WorkResponse struct {
	Email       string `json:"email"`
	Version     string `json:"version"`
	IPFSID      string `json:"ipfs_id"`
	IPFSVersion string `json:"ipfs_ver"`
	Online      bool   `json:"online"`
	Peers       int    `json:"peers,string"`

	Downloaded *string `json:"downloaded,omitempty"`
	Length     *int    `json:"length,omitempty"`
	Error      *int    `json:"error,omitempty"`
	Pinned     *string `json:"pinned,omitempty"`
	Deleted    *string `json:"deleted,omitempty"`

	Used  *int `json:"used,omitempty"`
	Avail *int `json:"avail,omitempty"`
}

func (r WorkResponse) String() string {
	sb := new(strings.Builder)

	encoder := json.NewEncoder(sb)

	_ = encoder.Encode(r)

	return sb.String()
}

type Work struct {
	Show     string `json:"show"`
	Episode  string `json:"episode"`
	Download string `json:"download"`
	Pin      string `json:"pin"`
	Filename string `json:"filename"`
	Delete   string `json:"delete"`
	Message  string `json:"message"`
}

func (w Work) String() string {
	sb := new(strings.Builder)

	encoder := json.NewEncoder(sb)

	_ = encoder.Encode(w)

	return sb.String()
}

func boolToStr(b bool) string {
	if b {
		return "true"
	}

	return "false"
}

func (r WorkResponse) Reader() io.Reader {
	data := url.Values{
		"email":    {r.Email},
		"version":  {r.Version},
		"ipfs_id":  {r.IPFSID},
		"ipfs_ver": {r.IPFSVersion},
		"online":   {boolToStr(r.Online)},
		"peers":    {strconv.Itoa(r.Peers)},
	}

	if r.Downloaded != nil {
		data.Set("downloaded", *r.Downloaded)
	}
	if r.Length != nil {
		data.Set("length", strconv.Itoa(*r.Length))
	}
	if r.Error != nil {
		data.Set("error", strconv.Itoa(*r.Error))
	}
	if r.Pinned != nil {
		data.Set("pinned", *r.Pinned)
	}
	if r.Deleted != nil {
		data.Set("deleted", *r.Deleted)
	}
	if r.Used != nil {
		data.Set("used", strconv.Itoa(*r.Used))
	}
	if r.Avail != nil {
		data.Set("avail", strconv.Itoa(*r.Avail))
	}

	return strings.NewReader(data.Encode())
}

func requestWork(client *http.Client, workResponse WorkResponse) (*Work, error) {
	retries := 5

	for {
		resp, err := client.Post(
			"https://ipfspodcasting.net/request",
			"application/x-www-form-urlencoded",
			workResponse.Reader(),
		)
		if err != nil {
			if retries > 0 && strings.Contains(err.Error(), "EOF") {
				time.Sleep(5 * time.Second)
				retries -= 1
				continue
			}

			return nil, fmt.Errorf("fetching work failed: %w", err)
		}
		defer resp.Body.Close()

		decoder := json.NewDecoder(resp.Body)
		var work Work

		err = decoder.Decode(&work)
		if err != nil {
			return nil, fmt.Errorf("decoding work failed: %w", err)
		}

		return &work, nil
	}
}

func responseWork(client *http.Client, workResponse WorkResponse) error {
	retries := 5

	for {
		resp, err := client.Post(
			"https://ipfspodcasting.net/response",
			"application/x-www-form-urlencoded",
			workResponse.Reader(),
		)
		if err != nil {
			if retries > 0 && strings.Contains(err.Error(), "EOF") {
				time.Sleep(5 * time.Second)
				retries -= 1
				continue
			}

			return fmt.Errorf("fetching work failed: %w", err)
		}

		resp.Body.Close()

		return nil
	}
}