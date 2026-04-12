import { startServer } from '../lsp/server.js';

// Ensure --stdio flag for vscode-languageserver transport detection
if (!process.argv.includes("--stdio")) {
  process.argv.push("--stdio");
}
startServer();
