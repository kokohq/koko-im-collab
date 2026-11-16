# Third-Party Dependencies

This document lists all third-party dependencies used in the Koko application, their versions, and their respective licenses.

## Production Dependencies

### github.com/lib/pq v1.10.9
- **Description:** PostgreSQL driver for Go
- **License:** BSD 3-Clause License
- **Repository:** https://github.com/lib/pq
- **Purpose:** Database connectivity and query execution for PostgreSQL backend
- **License Text:** 
```
Copyright (c) 2011-2024 Heroku
All rights reserved.

Redistribution and use in source and binary forms, with or without
modification, are permitted provided that the following conditions are met:

1. Redistributions of source code must retain the above copyright notice, this
   list of conditions and the following disclaimer.

2. Redistributions in binary form must reproduce the above copyright notice,
   this list of conditions and the following disclaimer in the documentation
   and/or other materials provided with the distribution.

3. Neither the name of the copyright holder nor the names of its
   contributors may be used to endorse or promote products derived from
   this software without specific prior written permission.

THIS SOFTWARE IS PROVIDED BY THE COPYRIGHT HOLDERS AND CONTRIBUTORS "AS IS"
AND ANY EXPRESS OR IMPLIED WARRANTIES, INCLUDING, BUT NOT LIMITED TO, THE
IMPLIED WARRANTIES OF MERCHANTABILITY AND FITNESS FOR A PARTICULAR PURPOSE ARE
DISCLAIMED. IN NO EVENT SHALL THE COPYRIGHT HOLDER OR CONTRIBUTORS BE LIABLE
FOR ANY DIRECT, INDIRECT, INCIDENTAL, SPECIAL, EXEMPLARY, OR CONSEQUENTIAL
DAMAGES (INCLUDING, BUT NOT LIMITED TO, PROCUREMENT OF SUBSTITUTE GOODS OR
SERVICES; LOSS OF USE, DATA, OR PROFITS; OR BUSINESS INTERRUPTION) HOWEVER
CAUSED AND ON ANY THEORY OF LIABILITY, WHETHER IN CONTRACT, STRICT LIABILITY,
OR TORT (INCLUDING NEGLIGENCE OR OTHERWISE) ARISING IN ANY WAY OUT OF THE USE
OF THIS SOFTWARE, EVEN IF ADVISED OF THE POSSIBILITY OF SUCH DAMAGE.
```

## Standard Library

The application also uses the following packages from the Go standard library, which are freely available under the BSD 3-Clause License (same as Go runtime):

- `context` - Context handling for cancellation and deadlines
- `crypto/hmac` - HMAC message authentication codes
- `crypto/sha256` - SHA-256 hashing
- `database/sql` - Standard SQL database interface
- `encoding/base64` - Base64 encoding/decoding
- `encoding/hex` - Hexadecimal encoding
- `encoding/json` - JSON encoding/decoding
- `errors` - Error handling
- `fmt` - Formatted I/O
- `io` - Core I/O interfaces
- `log` - Logging functionality
- `math` - Mathematical constants and functions
- `net` - Network functionality
- `net/http` - HTTP client and server
- `net/url` - URL parsing and handling
- `os` - Operating system interfaces
- `regexp` - Regular expressions
- `sort` - Sorting algorithms
- `strconv` - String conversions
- `strings` - String operations
- `sync` - Synchronization primitives (Mutex, RWMutex, etc.)
- `time` - Time handling
- `syscall` - System call interfaces

## License Compatibility

| Dependency | License | Compatible with MIT License |
|-----------|---------|------|
| github.com/lib/pq | BSD 3-Clause | ✅ Yes |

**Note:** All dependencies are compatible with the MIT License used by Koko. Both MIT and BSD 3-Clause licenses are permissive open-source licenses with complementary terms.

## Adding New Dependencies

When adding new dependencies to the Koko project:

1. Verify the dependency's license is compatible with MIT (permissive licenses preferred)
2. Run `go mod tidy` to update `go.mod` and `go.sum`
3. Update this `DEPENDENCIES.md` file with the new dependency information
4. Include the full license text if the license is uncommon

## License Verification

To verify the licenses of all dependencies, run:

```bash
go mod why github.com/lib/pq
go mod graph | grep lib/pq
```

To generate a more detailed dependency report:

```bash
go list -m all
```

## Compliance

This project complies with all applicable open-source license requirements:

- ✅ All third-party licenses are documented
- ✅ License texts are included or referenced
- ✅ Copyright notices are preserved
- ✅ Modifications (if any) are clearly indicated
- ✅ Distribution terms are respected

---

**Last Updated:** March 29, 2026  
**Koko Application Version:** 1.0  
**Application License:** MIT License
