{
  description = "Run an IPFS Podcasting node on NixOS";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixpkgs-unstable";

    devshell = {
      url = "github:numtide/devshell";
      inputs.nixpkgs.follows = "nixpkgs";
    };
    flake-parts = {
      url = "github:hercules-ci/flake-parts";
    };
    gitignore = {
      url = "github:hercules-ci/gitignore.nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = inputs@{
    self,
    nixpkgs,
    devshell,
    flake-parts,
    gitignore,
    ...
  }:
    flake-parts.lib.mkFlake { inherit inputs; } {
      imports = [
        devshell.flakeModule
        flake-parts.flakeModules.easyOverlay
      ];

      systems = [
        "x86_64-linux"
        "aarch64-linux"
        "x86_64-darwin"
        "aarch64-darwin"
      ];

      perSystem = { config, pkgs, system, ... }: let
        inherit (gitignore.lib) gitignoreSource;
      in {
        # Attrs for easyOverlay
        overlayAttrs = {
          inherit (config.packages)
            ipfspodcastingUpdater;
        };

        packages = {
          ipfspodcastingUpdater = pkgs.buildGo121Module {
            name = "ipfspodcasting-updater";

            src = gitignoreSource ./.;
            subPackages = [ "cmd/updater" ];

            vendorHash = "sha256-NlqLXSU9SwiCg6bt/+Q4qU/ST4mgqzSbhyaKr57f1Fg=";

            doCheck = false;

            CGO_ENABLED = 0;

            ldflags = [
              "-s"
              "-w"
              "-extldflags -static"
            ];
          };
        };

        devshells.default = {
          packages = with pkgs; [
            go_1_21
            golangci-lint
            kubo
          ];
        };
      };

      flake = rec {
        nixosModules.default = nixosModules.ipfspodcasting;
        nixosModules.ipfspodcasting = { config, lib, pkgs, ... }:
        with lib;
        let
          cfg = config.services.ipfspodcasting;
        in {
          options.services.ipfspodcasting = {
            enable = mkEnableOption (self.flake.description);

            dataDir = mkOption {
              type = types.path;
              default = "/var/lib/ipfs";
              description = "Kubo data directory";
            };

            email = mkOption {
              type = types.str;
              description = "Email address for managing the node via https://ipfspodcasting.net/manage";
            };

            apiAddress = mkOption {
              type = types.str;
              default = "/ip4/127.0.0.1/tcp/5001";
              description = "API address for Kubo";
            };

            metricsAddress = mkOption {
              type = types.str;
              default = "0.0.0.0";
              description = "Address of the metrics server";
            };

            metricsPort = mkOption {
              type = types.port;
              default = 9196;
              description = "Port number of the metrics server";
            };

            httpTimeout = mkOption {
              type = types.str;
              default = "5m";
              description = "HTTP timeout. Applies to downloads and requests to Kubo";
            };

            openFirewall = mkOption {
              type = types.bool;
              default = true;
              description = "Open the P2P port for Kubo";
            };

            user = mkOption {
              type = types.str;
              default = "ipfs";
              description = "System user to be used for Kubo and the IPFS Podcasting Updater";
            };

            group = mkOption {
              type = types.str;
              default = "ipfs";
              description = "System group to be used for Kubo and the IPFS Podcasting Updater";
            };

            kuboSettings = mkOption {
              type = (pkgs.formats.json {}).type;
              default = {};
              description = "Settings for Kubo. Will turn into the settings file.";
            };
          };

          config = mkIf cfg.enable {
            networking.firewall = mkIf cfg.openFirewall {
              allowedTCPPorts = [
                4001
                cfg.metricsPort
              ];
              allowedUDPPorts = [
                4001
              ];
            };

            services.kubo = {
              enable = true;
              dataDir = cfg.dataDir;
              # Bug in NixOS deployment, we have to define else Kubo panics
              settings = mkMerge [
                cfg.kuboSettings
                {
                  Addresses.API = [ cfg.apiAddress ];
                }
              ];
              user = cfg.user;
              group = cfg.group;
            };

            systemd.services.ipfspodcasting-updater = {
              description = "Updates IPFS Podcasting pinned files";
              wantedBy = [ "multi-user.target" ];
              after = [ "network.target" "ipfs.service" ];

              serviceConfig = let
                args = [
                  "--api-address='${cfg.apiAddress}'"
                  "--email='${cfg.email}'"
                  "--http-timeout='${cfg.httpTimeout}'"
                  "--metrics-address='${cfg.metricsAddress}:${toString cfg.metricsPort}'"
                ];
              in {
                ExecStart = "${pkgs.ipfspodcastingUpdater}/bin/updater ${concatStringsSep " " args}";

                User = cfg.user;
                Group = cfg.group;

                Restart = "on-failure";
              };
            };

            # https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes
            boot.kernel.sysctl = {
              "net.core.rmem_max" = 2500000;
              "net.core.wmem_max" = 2500000;
            };
          };
        };
      };
    };
}
