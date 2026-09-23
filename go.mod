module github.com/bnema/nefertty

go 1.27

require (
	github.com/BurntSushi/toml v1.5.0
	github.com/bnema/purego-libwayland v0.0.0
	github.com/bnema/wlturbo v0.2.0
	github.com/bnema/zerowrap v1.4.1
	github.com/rs/zerolog v1.35.1
	golang.org/x/sys v0.48.0
	golang.org/x/term v0.46.0
)

require (
	github.com/bnema/purego v0.11.0-bnema.4 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.22 // indirect
	gopkg.in/natefinch/lumberjack.v2 v2.2.1 // indirect
)

replace github.com/bnema/purego-libwayland => ../purego-libwayland
