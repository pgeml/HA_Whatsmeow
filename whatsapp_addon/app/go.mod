module github.com/youruser/whatsapp_addon

go 1.25.0
toolchain go1.25.5

require (
    github.com/hajimehoshi/go-mp3 v0.3.4
    github.com/jfreymuth/oggvorbis v1.0.5
    github.com/mdp/qrterminal/v3 v3.2.1
    go.mau.fi/whatsmeow v0.0.0
    google.golang.org/protobuf v1.36.11
    modernc.org/sqlite v1.49.1
    rsc.io/qr v0.2.0
)

replace go.mau.fi/whatsmeow => ./whatsmeow
