// Copyright 2020 Coinbase, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package cmd

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/coinbase/rosetta-ethereum/configuration"
	"github.com/coinbase/rosetta-ethereum/optimism"
	"github.com/coinbase/rosetta-ethereum/services"

	"github.com/coinbase/rosetta-sdk-go/asserter"
	"github.com/coinbase/rosetta-sdk-go/server"
	"github.com/coinbase/rosetta-sdk-go/types"
	"github.com/spf13/cobra"
	"golang.org/x/sync/errgroup"
)

const (
	// readTimeout is the maximum duration for reading the entire
	// request, including the body.
	readTimeout = 5 * time.Second

	// idleTimeout is the maximum amount of time to wait for the
	// next request when keep-alives are enabled.
	idleTimeout = 30 * time.Second
)

var (
	runCmd = &cobra.Command{
		Use:   "run",
		Short: "Run rosetta-ethereum",
		RunE:  runRunCmd,
	}
)

func runRunCmd(cmd *cobra.Command, args []string) error {
	cfg, err := configuration.LoadConfiguration()
	if err != nil {
		return fmt.Errorf("%w: unable to load configuration", err)
	}

	// The asserter automatically rejects incorrectly formatted
	// requests.
	asserter, err := asserter.NewServer(
		optimism.OperationTypes,
		optimism.HistoricalBalanceSupported,
		[]*types.NetworkIdentifier{cfg.Network},
		optimism.CallMethods,
		optimism.IncludeMempoolCoins,
		"",
	)
	if err != nil {
		return fmt.Errorf("%w: could not initialize server asserter", err)
	}

	// Start required services
	ctx := context.Background()
	ctx, cancel := context.WithCancel(ctx)
	go handleSignals([]context.CancelFunc{cancel})

	g, ctx := errgroup.WithContext(ctx)

	var client *optimism.Client
	if cfg.Mode == configuration.Online {
		if !cfg.RemoteGeth {
			g.Go(func() error {
				return optimism.StartGeth(ctx, cfg.GethArguments, g)
			})
		}

		opts := optimism.ClientOptions{
			HTTPTimeout:         cfg.L2GethHTTPTimeout,
			MaxTraceConcurrency: cfg.MaxConcurrentTraces,
			EnableTraceCache:    cfg.EnableTraceCache,
			EnableGethTracer:    cfg.EnableGethTracer,
			SupportedTokens:     getSupportedTokens(cfg.Network.Network),
		}
		var err error
		client, err = optimism.NewClient(cfg.GethURL, cfg.Params, opts)
		if err != nil {
			return fmt.Errorf("%w: cannot initialize ethereum client", err)
		}
		defer client.Close()
	}

	router := services.NewBlockchainRouter(cfg, client, asserter)

	loggedRouter := server.LoggerMiddleware(router)
	corsRouter := server.CorsMiddleware(loggedRouter)
	server := &http.Server{
		Addr:         fmt.Sprintf(":%d", cfg.Port),
		Handler:      corsRouter,
		ReadTimeout:  readTimeout,
		WriteTimeout: cfg.L2GethHTTPTimeout,
		IdleTimeout:  idleTimeout,
	}

	g.Go(func() error {
		log.Printf("server listening on port %d", cfg.Port)
		return server.ListenAndServe()
	})

	g.Go(func() error {
		// If we don't shutdown server in errgroup, it will
		// never stop because server.ListenAndServe doesn't
		// take any context.
		<-ctx.Done()

		return server.Shutdown(ctx)
	})

	err = g.Wait()
	if SignalReceived {
		return errors.New("rosetta-ethereum halted")
	}

	return err
}

func getSupportedTokens(network string) map[string]bool {
	switch network {
	case optimism.MainnetNetwork:
		return map[string]bool{
			"0x4200000000000000000000000000000000000042": true, // OP
			"0xda10009cbd5d07dd0cecc66161fc93d7c9000da1": true, // DAI
			"0x8700daec35af8ff88c16bdf0418774cb3d7599b4": true, // SNX
			"0x94b008aa00579c1307b0ef2c499ad98a8ce58e58": true, // USDT
			"0x68f180fcce6836688e9084f035309e29bf0a2095": true, // WBTC
			"0x7f5c764cbc14f9669b88837ca1490cca17c31607": true, // USDC
		}
	case optimism.TestnetNetwork: // Goerli - 420
		return map[string]bool{
			"0x4200000000000000000000000000000000000042": true, // OP
			"0xda10009cbd5d07dd0cecc66161fc93d7c9000da1": true, // DAI
			"0x2e5ed97596a8368eb9e44b1f3f25b2e813845303": true, // SNX
			"0x853eb4ba5d0ba2b77a0a5329fd2110d5ce149ece": true, // USDT
			"0xe0a592353e81a94db6e3226fd4a99f881751776a": true, // WBTC
			"0x7e07e15d2a87a24492740d16f5bdf58c16db0c4e": true, // USDC
		}
	default:
		return map[string]bool{
			"0x4200000000000000000000000000000000000042": true, // OP
		}
	}
}
