# SkyChess

Sky.Live chess game with an AI opponent using 2-ply minimax search. Uses proper ADT types for Kind, Colour, and Piece. Game state is persisted to SQLite.

## Build & Run

```bash
sky install
sky build src/Main.sky
./sky-out/app
```

Open `http://localhost:8000` in your browser. Play as white against the AI.
