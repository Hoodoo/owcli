# Files

- [Architecture Overview](overview.md) - How owcli is layered into Go packages, how a command flows from the CLI through generation, Claims, OKF finalization, and search, and what "compatible with upstream OpenWiki" means.
- [Storage Layouts and Bindings](storage-and-bindings.md) - Where owcli keeps a wiki and its control state, how repositories are bound in-repo or externally (writing nothing into the repository), how state files are persisted atomically, and how .openwikiignore excludes paths.
