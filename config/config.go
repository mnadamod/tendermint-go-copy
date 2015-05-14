package config

import (
	"bufio"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"

	flag "github.com/spf13/pflag"
	"github.com/tendermint/confer"
)

var app *confer.Config
var appMtx sync.Mutex

func App() *confer.Config {
	appMtx.Lock()
	defer appMtx.Unlock()
	if app == nil {
		Init("")
	}
	return app
}

func SetApp(a *confer.Config) {
	appMtx.Lock()
	defer appMtx.Unlock()
	app = a
}

// NOTE: If you change this, maybe also change initDefaults()
var defaultConfigTmpl = `# This is a TOML config file.
# For more information, see https://github.com/toml-lang/toml

network = "tendermint_testnet_5"
moniker = "__MONIKER__"
node_laddr = "0.0.0.0:46656"
seeds = "goldenalchemist.chaintest.net:46656"
fast_sync = true
db_backend = "leveldb"
log_level = "debug"
rpc_laddr = "0.0.0.0:46657"
`

var DefaultGenesis = `{
    "accounts": [
        {
            "address": "F81CB9ED0A868BD961C4F5BBC0E39B763B89FCB6",
            "amount": 690000000000
        },
        {
            "address": "0000000000000000000000000000000000000002",
            "amount": 565000000000
        },
        {
            "address": "9E54C9ECA9A3FD5D4496696818DA17A9E17F69DA",
            "amount": 525000000000
        },
        {
            "address": "0000000000000000000000000000000000000004",
            "amount": 110000000000
        }
    ],
    "validators": [
        {
            "pub_key": [1, "178EC6008A4364508979C70CBF100BD4BCBAA12DDE6251F5F486B4FD09014F06"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "93E243AC8A01F723DE353A4FA1ED911529CCB6E5",
                    "amount":  5000000000
                }
            ]
        },
        {
            "pub_key": [1, "2A77777CC51467DE42350D4A8F34720D527734189BE64C7A930DD169E1FED3C6"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "93E243AC8A01F723DE353A4FA1ED911529CCB6E5",
                    "amount":  5000000000
                }
            ]
        },
        {
            "pub_key": [1, "3718E69D09B11B3AD3FA31AEF07EC416D2AEED241CACE7B0F30AE9803FFB0F08"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "93E243AC8A01F723DE353A4FA1ED911529CCB6E5",
                    "amount":  5000000000
                }
            ]
        },
        {
            "pub_key": [1, "C6B0440DEACD1E4CF1C736CEB8E38E788B700BA2B2045A55CB657A455CF5F889"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "93E243AC8A01F723DE353A4FA1ED911529CCB6E5",
                    "amount":  5000000000
                }
            ]
        },
        {
            "pub_key": [1, "3BA1190D54F91EFBF8B0125F7EC116AD4BA2894B6EE38564A5D5FD3230D91F7B"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "93E243AC8A01F723DE353A4FA1ED911529CCB6E5",
                    "amount":  5000000000
                }
            ]
        },
        {
            "pub_key": [1, "E56663353D01C58A1D4CDB4D14B70C2E3335BE1EBB6C3F697AF7882C03837962"],
            "amount": 5000000000,
            "unbond_to": [
                {
                    "address": "9E54C9ECA9A3FD5D4496696818DA17A9E17F69DA",
                    "amount":  5000000000
                }
            ]
        }
    ]
}`

// If not defined in the process args nor config file, then use these defaults.
// NOTE: If you change this, maybe also change defaultConfig
func initDefaults(rootDir string) {
	app.SetDefault("network", "tendermint_testnet0")
	app.SetDefault("version", "0.2.1")
	app.SetDefault("genesis_file", rootDir+"/genesis.json")
	app.SetDefault("moniker", "anonymous")
	app.SetDefault("node_laddr", "0.0.0.0:46656")
	app.SetDefault("seeds", "goldenalchemist.chaintest.net:46656")
	app.SetDefault("fast_sync", true)
	app.SetDefault("addrbook_file", rootDir+"/addrbook.json")
	app.SetDefault("priv_validator_file", rootDir+"/priv_validator.json")
	app.SetDefault("db_backend", "leveldb")
	app.SetDefault("db_dir", rootDir+"/data")
	app.SetDefault("log_level", "info")
	app.SetDefault("rpc_laddr", "0.0.0.0:46657")
}

func Init(rootDir string) {

	// Get rootdir
	if rootDir == "" {
		rootDir = os.Getenv("TMROOT")
	}
	if rootDir == "" {
		rootDir = os.Getenv("HOME") + "/.tendermint"
	}
	configFile := path.Join(rootDir, "config.toml")
	genesisFile := path.Join(rootDir, "genesis.json")

	// Write default config file if missing.
	if !fileExists(configFile) {
		// Ask user for moniker
		moniker := getInput("Type hostname: ", "anonymous")
		defaultConfig := strings.Replace(defaultConfigTmpl, "__MONIKER__", moniker, -1)
		writeFile(configFile, defaultConfig)
	}
	if !fileExists(genesisFile) {
		writeFile(genesisFile, DefaultGenesis)
	}

	// Initialize Config
	app = confer.NewConfig()
	initDefaults(rootDir)
	paths := []string{configFile}
	if err := app.ReadPaths(paths...); err != nil {
		log.Warn("Error reading configuration", "paths", paths, "error", err)
	}

	// Confused?
	//app.Debug()
}

func getInput(prompt string, defaultValue string) string {
	fmt.Print(prompt)
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil {
		log.Warn("Error reading stdin", "err", err)
		return defaultValue
	} else {
		line = strings.TrimSpace(line)
		if line == "" {
			return defaultValue
		}
		return line
	}
}

func fileExists(file string) bool {
	_, err := os.Stat(file)
	return !os.IsNotExist(err)
}

func writeFile(file, contents string) {
	if _, err := os.Stat(file); os.IsNotExist(err) {
		if strings.Index(file, "/") != -1 {
			err := os.MkdirAll(filepath.Dir(file), 0700)
			if err != nil {
				fmt.Printf("Could not create directory: %v", err)
				os.Exit(1)
			}
		}
		err := ioutil.WriteFile(file, []byte(contents), 0600)
		if err != nil {
			fmt.Printf("Could not write file: %v", err)
			os.Exit(1)
		}
		fmt.Printf("File written to %v.\n", file)
	}
}

func ParseFlags(args []string) {
	var flags = flag.NewFlagSet("main", flag.ExitOnError)
	var printHelp = false

	// Declare flags
	flags.BoolVar(&printHelp, "help", false, "Print this help message.")
	flags.String("moniker", app.GetString("moniker"), "Node Name")
	flags.String("node_laddr", app.GetString("node_laddr"), "Node listen address. (0.0.0.0:0 means any interface, any port)")
	flags.String("seeds", app.GetString("seeds"), "Comma delimited seed nodes")
	flags.Bool("fast_sync", app.GetBool("fast_sync"), "Fast blockchain syncing")
	flags.String("rpc_laddr", app.GetString("rpc_laddr"), "RPC listen address. Port required")
	flags.String("log_level", app.GetString("log_level"), "Log level")
	flags.Parse(args)
	if printHelp {
		flags.PrintDefaults()
		os.Exit(0)
	}

	// Merge parsed flag values onto app.
	app.BindPFlag("moniker", flags.Lookup("moniker"))
	app.BindPFlag("node_laddr", flags.Lookup("node_laddr"))
	app.BindPFlag("seeds", flags.Lookup("seeds"))
	app.BindPFlag("fast_sync", flags.Lookup("fast_sync"))
	app.BindPFlag("rpc_laddr", flags.Lookup("rpc_laddr"))
	app.BindPFlag("log_level", flags.Lookup("log_level"))

	// Confused?
	//app.Debug()
}
