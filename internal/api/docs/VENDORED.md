# Vendored Scalar assets

Self-hosted API reference UI for `/docs`. No CDN calls at runtime: the
bundle and every font it can request are served from this directory.

- scalar-1.68.0.js: `@scalar/api-reference@1.68.0` `dist/browser/standalone.js`
  - tarball: https://registry.npmjs.org/@scalar/api-reference/-/api-reference-1.68.0.tgz
  - sha512: rY43w3REwCxp+rDDx/0CncZxmlzISnGTK9zZ8moq0Ij2vRHhLQCJ0/BXut9pBAupVrOZF7MoqKXcG+gISgTu5g==
  - patch: `https://fonts.scalar.com/` rewritten to `/docs/assets/fonts/`
- fonts/*.woff2: fetched from `https://fonts.scalar.com/` (Inter and
  Scalar Mono subsets referenced by the bundle)

To upgrade: download the new tarball, verify its `dist.integrity` sha512
against the npm registry metadata, replace the js, re-apply the font URL
rewrite, and re-check the referenced font list with
`grep -o 'fonts.scalar.com/[a-z-]*\.woff2'`.
