# Fyne Stopwatch

A desktop stopwatch built with the [Fyne](https://fyne.io) toolkit. The window
shows the elapsed time and two buttons: **Start** (it reads **Pause** while the
clock runs) and **Reset**.

It shows the two ways a Sky program uses Go:

- **Generated bindings for a Go package.** `src/Main.sky` creates the Fyne app
  and the window, sizes it and runs it through the bindings for
  `fyne.io/fyne/v2` and `fyne.io/fyne/v2/app`.
- **A local Go module.** `stopwatch/` is a small Go module (`go.mod` declares
  `example.local/stopwatch`) that builds the stopwatch's widgets and runs its
  clock, which needs a goroutine and a mutex. It was added with
  `sky add ./stopwatch`, which records it in `sky.toml` under
  `["go.dependencies"]` and generates the typed binding
  `Example.Local.Stopwatch.content : () -> Result Error CanvasObject`.

Every Go call returns `Result Error a`, so `Main.sky` chains the steps with
`Result.andThen` and reports the first failure. See
[docs/ffi/boundary-philosophy.md](../../docs/ffi/boundary-philosophy.md).

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Requires Go and a C compiler, plus Fyne's system dependencies (OpenGL, and the
X11 headers on Linux). See [Fyne prerequisites](https://docs.fyne.io/started/)
for your platform.

## Test the stopwatch

The Go module has a headless test that uses Fyne's test driver: it checks that
the window holds the display and both buttons, and that Start, Pause and Reset
drive the clock.

```bash
cd stopwatch && go test ./...
```
