{
  description = "Speech-to-text daemon for i3 (whisper.cpp, local-only)";

  inputs = {
    nixpkgs.url = "github:nixos/nixpkgs/nixos-25.11";
    nixpkgs-unstable.url = "github:nixos/nixpkgs/nixpkgs-unstable";
    stapelbergnix = {
      url = "github:stapelberg/nix";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs =
    {
      self,
      nixpkgs,
      nixpkgs-unstable,
      stapelbergnix,
    }:
    let
      supportedSystems = [
        "x86_64-linux"
        "aarch64-linux"
      ];
      forAllSystems = nixpkgs.lib.genAttrs supportedSystems;
    in
    {
      packages = forAllSystems (
        system:
        let
          pkgs-unstable = import nixpkgs-unstable {
            inherit system;
            overlays = [ stapelbergnix.overlays.goVcsStamping ];
          };
        in
        {
          default = pkgs-unstable.buildGoLatestModule {
            pname = "stt-for-i3";
            version = "unstable";
            src = self;
            env.CGO_ENABLED = "0";
            vendorHash = "sha256-vcHF3Z5f0z/B67StD1ohtRkxFNJAUoF2g2SsPDFLWBE=";
            doCheck = false;
          };
        }
      );

      nixosModules.default =
        {
          config,
          lib,
          pkgs,
          ...
        }:
        let
          cfg = config.services.stt-for-i3;
        in
        {
          options.services.stt-for-i3 = {
            enable = lib.mkEnableOption "stt-for-i3 speech-to-text daemon";

            package = lib.mkOption {
              type = lib.types.package;
              default = self.packages.${pkgs.system}.default;
              description = "The stt-for-i3 package to use.";
            };

            whisperPackage = lib.mkOption {
              type = lib.types.package;
              default = pkgs.whisper-cpp;
              description = "The whisper-cpp package providing whisper-cli.";
            };
          };

          config = lib.mkIf cfg.enable {
            environment.systemPackages = [ cfg.package ];

            systemd.user.services.stt-for-i3 = {
              description = "Speech-to-text daemon for i3";
              after = [ "graphical-session.target" ];
              partOf = [ "graphical-session.target" ];
              wantedBy = [ "graphical-session.target" ];
              serviceConfig = {
                Type = "notify";
                ExecStart = "${cfg.package}/bin/stt-for-i3 daemon";
                Restart = "on-failure";
                RestartSec = 1;
                NotifyAccess = "main";
                TimeoutStopSec = 30;
              };
              path = [
                pkgs.alsa-utils # arecord
                cfg.whisperPackage # whisper-cli
                pkgs.xclip
                pkgs.xdotool
                pkgs.dunst # dunstify
              ];
            };
          };
        };

      formatter = forAllSystems (
        system:
        let
          pkgs = import nixpkgs { inherit system; };
        in
        pkgs.nixfmt-tree
      );
    };
}
