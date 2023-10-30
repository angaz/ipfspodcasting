# IPFS Podcasting

This project contains an updater for [IPFS Podcasting][ipfspodcasting] and a
NixOS module for configuring [Kubo][kubo] and the updater.

### IPFS Podcasting Updater

Looks for updates from [IPFS Podcasting][ipfspodcasting], and downloads, pins,
or deletes the episode, depending on the instructions from the server.

This was based on the original [Python script][updater-script], which I felt
could be improved.

It runs as a service instead of a cronjob or timer, and it manages the update
cycle. It waits a small period between each episode downloaded, and a longer,
configurable time between updates where there was nothing to do. So the initial
sync is much faster.

It also uses the HTTP API instead of using subprocesses, which is more likely
to work on more systems and requires no dependencies.

### NixOS Module

The Nix Flake also contains a NixOS module, so you can install and configure
the updater on your NixOS installation.

An example configuration:

```nix
{
  inputs = {
    ipfspodcasting = {
      url = "github:angaz/ipfspodcasting";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  }

  outputs = {
    ipfspodcasting,
    ...
  }: {
    nixosConfigurations = {
      ipfsNode = nixpkgs.lib.nixosSystem {
        specialArgs = {
          inherit ipfspodcasting;
        };
        modules = [
          ({ ipfspodcasting, ... }: {
            nixpkgs.overlays = [
              ipfspodcasting.overlays.default
            ];

            services.ipfspodcasting = {
              enable = true;
              email = "email@example.com";
              kuboSettings = {
                Datastore.StorageMax = "1000GB";
              };
            };
          })
          ipfspodcasting.nixosModules.default
        ];
      };
    };
  };
}
```

[ipfspodcasting]: https://ipfspodcasting.net
[upfster-script]: https://github.com/Cameron-IPFSPodcasting/podcastnode-Python/blob/main/ipfspodcastnode.py
