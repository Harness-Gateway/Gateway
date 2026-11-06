# Bundled Scalar API Reference

The unmodified standalone browser bundle is from `@scalar/api-reference@1.70.0`.
It is embedded in the gateway; users do not need npm or network access to load
the documentation UI. Provider inference still uses the authenticated CLIs.

Version 1.70.0 is pinned because it passed repeated SSE requests, cancellation,
and process-cleanup browser tests. Version 1.77.0 emitted unhandled stream-reader
cleanup errors when sending another request after streaming. Keep this regression
covered when evaluating an upgrade; do not patch the minified bundle or suppress
browser errors.

- Package: <https://registry.npmjs.org/@scalar/api-reference/-/api-reference-1.70.0.tgz>
- npm integrity: `sha512-2oX0upq8YWANljR0b9ANgiOsaMxUxoyQyiFVuYBbvp1x4kvcahKWy2Vw+KZFXt6H/rFQC6dY8LAdszhQQfB5ww==`
- Package file: `package/dist/browser/standalone.js`
- Bundle SHA-256: `ab654f973a5e5aa5ba0cb0f53635db8c9b2a0f3b454a613238c4fa223b2046e2`
- License: MIT, retained in `LICENSE.scalar` and served at `/docs/assets/LICENSE.scalar`.
- License source: <https://raw.githubusercontent.com/scalar/scalar/dff52dcbf0b5b293c086a584fb74b5e4171e5a5e/LICENSE>

To update, select a specific npm version, use `npm pack` to verify and download
the package, and extract its standalone browser bundle. Preserve the upstream
license, record its integrity and SHA-256 here, and update the versioned filename,
embed directive, HTML reference, route, and bundle-integrity test together.
Do not edit the generated bundle by hand. Verify the UI in a browser with
external requests blocked, including streamed output and request cancellation.
