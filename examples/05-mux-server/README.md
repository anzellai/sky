# 05-mux-server

This example demonstrates how to build an HTTP server using [Gorilla Mux](https://github.com/gorilla/mux) and standard Go library `net/http` from Sky language.

It also showcases how to use `github.com/joho/godotenv` to load environment variables from a `.env` file, defaulting to port `5000` if not present.

## Running the example

```bash
# Optional: Install dependencies if not already done
sky install

# Build and run the server
sky build src/Main.sky
./dist/app
```

Then test the endpoints in another terminal:

```bash
curl http://localhost:5000/
curl http://localhost:5000/echo
curl http://localhost:5000/ping
```
