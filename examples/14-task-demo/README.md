# Task Demo

Demonstrates the Task effect boundary -- the separation between pure functions and effectful operations (file I/O, HTTP requests, randomness).

## Build & Run

```bash
sky build src/Main.sky
./sky-out/app
```

Shows how `Task.andThen`, `Task.map`, `Task.sequence`, and `Task.parallel` compose effectful computations.
