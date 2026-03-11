// src/module-resolver.ts
// Sky compiler module graph + file resolver
//
// Responsibilities:
// - resolve `import Foo.Bar` -> <project>/src/Foo/Bar.sky (and fallback variants)
// - load, lex, parse imported modules
// - build a full dependency graph from an entry module
// - detect duplicate module names
// - detect import cycles
// - surface consistent diagnostics
//
// Important:
// - only `module.imports` participate in Sky module resolution
// - `ForeignImportDeclaration` is NOT a Sky module dependency and must be ignored here

import fs from "fs";
import path from "path";
import process from "process";

import { lex, type Diagnostic, type SourceSpan } from "./lexer.js";
import { parse } from "./parser.js";
import { filterLayout } from "./parser/filter-layout.js";
import type { Module, ImportDeclaration } from "./ast.js";
import { resolveNpmImport } from "./ffi/resolve-npm-import.js"

export interface ModuleResolverOptions {
  readonly projectRoot: string;
  readonly sourceRoot?: string;
  readonly extensions?: readonly string[];
}

export interface ResolvedModule {
  readonly moduleName: string;
  readonly filePath: string;
  readonly source: string;
  readonly ast: Module;
}

export interface ModuleGraphNode {
  readonly moduleName: string;
  readonly filePath: string;
  readonly ast: Module;
  readonly dependencies: readonly string[];
}

export interface ModuleGraph {
  readonly entryModuleName: string;
  readonly nodes: ReadonlyMap<string, ModuleGraphNode>;
  readonly diagnostics: readonly Diagnostic[];
  readonly topologicalOrder: readonly string[];
}

const DEFAULT_EXTENSIONS = [".sky"] as const;

export class ModuleResolver {
  private readonly projectRoot: string;
  private readonly sourceRoot: string;
  private readonly extensions: readonly string[];
  private readonly diagnostics: Diagnostic[] = [];

  private readonly modulesByName = new Map<string, ModuleGraphNode>();
  private readonly moduleNameByFilePath = new Map<string, string>();
  private readonly visiting = new Set<string>();
  private readonly visited = new Set<string>();
  private readonly topologicalOrder: string[] = [];

  constructor(options: ModuleResolverOptions) {
    this.projectRoot = path.resolve(options.projectRoot);
    this.sourceRoot = path.resolve(options.sourceRoot ?? path.join(this.projectRoot, "src"));
    this.extensions = options.extensions ?? DEFAULT_EXTENSIONS;
  }

  public buildGraphFromEntry(entryFile: string): ModuleGraph {
    const fullEntryPath = path.resolve(entryFile);

    const entryResolved = this.loadModuleByFile(fullEntryPath);

    if (!entryResolved) {
      return {
        entryModuleName: "<unknown>",
        nodes: new Map(),
        diagnostics: [...this.diagnostics],
        topologicalOrder: [],
      };
    }

    this.walk(entryResolved.moduleName, entryResolved.filePath);

    return {
      entryModuleName: entryResolved.moduleName,
      nodes: this.modulesByName,
      diagnostics: [...this.diagnostics],
      topologicalOrder: [...this.topologicalOrder],
    };
  }

  private async walk(moduleName: string, filePath: string): Promise<void> {
    if (this.visited.has(moduleName)) {
      return;
    }

    if (this.visiting.has(moduleName)) {
      this.reportSynthetic(
        `Import cycle detected involving module ${moduleName}.`,
        `Break the cycle by extracting shared code into a separate module.`,
      );
      return;
    }

    this.visiting.add(moduleName);

    const loaded = this.loadModuleByFile(filePath);
    if (!loaded) {
      this.visiting.delete(moduleName);
      return;
    }

    // IMPORTANT:
    // Only regular Sky imports participate in module resolution.
    // Foreign imports are declarations, not filesystem modules.
    const dependencies = loaded.ast.imports.map((imp) => joinModuleName(imp.moduleName));

    const existing = this.modulesByName.get(moduleName);
    if (!existing) {
      this.modulesByName.set(moduleName, {
        moduleName,
        filePath: loaded.filePath,
        ast: loaded.ast,
        dependencies,
      });
    }

    for (const imp of loaded.ast.imports) {
      const importedName = joinModuleName(imp.moduleName);
      const importedFile = await this.resolveImportToFile(imp);

      if (!importedFile) {
        continue;
      }

      this.walk(importedName, importedFile);
    }

    this.visiting.delete(moduleName);
    this.visited.add(moduleName);
    this.topologicalOrder.push(moduleName);
  }

  private loadModuleByFile(filePath: string): ResolvedModule | undefined {
    const normalizedPath = path.resolve(filePath);

    const knownModuleName = this.moduleNameByFilePath.get(normalizedPath);
    if (knownModuleName) {
      const known = this.modulesByName.get(knownModuleName);
      if (known) {
        return {
          moduleName: known.moduleName,
          filePath: known.filePath,
          source: fs.readFileSync(known.filePath, "utf8"),
          ast: known.ast,
        };
      }
    }

    if (!fs.existsSync(normalizedPath)) {
      this.reportSynthetic(`Module file not found: ${normalizedPath}.`);
      return undefined;
    }

    const source = fs.readFileSync(normalizedPath, "utf8");

    const lexResult = lex(source, normalizedPath);
    if (lexResult.diagnostics.length > 0) {
      this.diagnostics.push(...lexResult.diagnostics);
      return undefined;
    }

    let ast: Module;
    try {
      ast = parse(filterLayout(lexResult.tokens));
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      this.reportSynthetic(`${normalizedPath}: ${message}`);
      return undefined;
    }

    const declaredModuleName = joinModuleName(ast.name);
    const expectedModuleName = this.deriveModuleNameFromFile(normalizedPath);

    if (declaredModuleName !== expectedModuleName) {
      this.diagnostics.push({
        severity: "error",
        message: `Declared module ${declaredModuleName} does not match file path-derived module name ${expectedModuleName}.`,
        span: ast.span,
        hint: `Rename the module or move the file so they match.`,
      });
      return undefined;
    }

    const existingPathForModule = this.findExistingPathForModule(declaredModuleName);
    if (existingPathForModule && existingPathForModule !== normalizedPath) {
      this.reportSynthetic(
        `Duplicate module ${declaredModuleName} found in both ${existingPathForModule} and ${normalizedPath}.`,
        `Each module name must be unique within the project.`,
      );
      return undefined;
    }

    this.moduleNameByFilePath.set(normalizedPath, declaredModuleName);

    const node: ModuleGraphNode = {
      moduleName: declaredModuleName,
      filePath: normalizedPath,
      ast,
      dependencies: ast.imports.map((imp) => joinModuleName(imp.moduleName)),
    };

    this.modulesByName.set(declaredModuleName, node);

    return {
      moduleName: declaredModuleName,
      filePath: normalizedPath,
      source,
      ast,
    };
  }


  private async resolveImportToFile(
    imp: ImportDeclaration
  ): Promise<string | undefined> {

    const importedModuleName =
      joinModuleName(imp.moduleName)

    const candidateBase =
      path.join(this.sourceRoot, ...imp.moduleName)

    const candidates: string[] = []

    for (const extension of this.extensions) {

      candidates.push(candidateBase + extension)

      candidates.push(
        path.join(candidateBase, "index" + extension)
      )

    }

    for (const candidate of candidates) {

      if (fs.existsSync(candidate)) {
        return path.resolve(candidate)
      }

    }

    // -------------------------
    // NPM fallback resolution
    // -------------------------

    const npmResolved =
      await resolveNpmImport(
        imp.moduleName[imp.moduleName.length - 1]
      )

    if (npmResolved) {
      return npmResolved
    }

    this.diagnostics.push({
      severity: "error",
      message: `Could not resolve import ${importedModuleName}.`,
      span: imp.span,
      hint: `Expected one of: ${candidates.join(", ")}`,
    })

    return undefined

  }


  private deriveModuleNameFromFile(filePath: string): string {
    const relative = path.relative(this.sourceRoot, filePath);
    const withoutExtension = removeKnownExtension(relative, this.extensions);
    const parts = withoutExtension.split(path.sep).filter(Boolean);
    return parts.join(".");
  }

  private findExistingPathForModule(moduleName: string): string | undefined {
    const node = this.modulesByName.get(moduleName);
    return node?.filePath;
  }

  private reportSynthetic(message: string, hint?: string): void {
    this.diagnostics.push({
      severity: "error",
      message,
      span: zeroSpan(),
      hint,
    });
  }
}

export function buildModuleGraph(
  entryFile: string,
  options?: Partial<ModuleResolverOptions>,
): ModuleGraph {
  const projectRoot = path.resolve(options?.projectRoot ?? process.cwd());

  const resolver = new ModuleResolver({
    projectRoot,
    sourceRoot: options?.sourceRoot,
    extensions: options?.extensions,
  });

  return resolver.buildGraphFromEntry(entryFile);
}

export function joinModuleName(parts: readonly string[]): string {
  return parts.join(".");
}

function removeKnownExtension(filePath: string, extensions: readonly string[]): string {
  for (const extension of extensions) {
    if (filePath.endsWith(extension)) {
      return filePath.slice(0, -extension.length);
    }
  }
  return filePath;
}

function zeroSpan(): SourceSpan {
  return {
    start: { offset: 0, line: 1, column: 1 },
    end: { offset: 0, line: 1, column: 1 },
  };
}
