# reauth-web login library

Source: https://github.com/boji1334/reauth-web

Pinned commit: `db8251817fd1baefc83c14c851135fb6078bc001` (MIT).

Only the login library and local TOTP generator are embedded; no Web UI, redemption service, telemetry, registration service or automatic upload is included. The Sub2API adapter preserves identity/workspace validation and worker limits. `http_client.py` is adapted to require the existing pinned curl-cffi runtime and to constrain request destinations and redirects.

Vendored Python trailing whitespace is normalized; the upstream logic is unchanged except for the HTTP adapter described above.

Original upstream SHA256 values:

```json
[
  {
    "upstream_path": "vendor/lib/oauth_client.py",
    "sha256": "ba6c44e851a16e1f00a2e028f33f531cd9dc7659e7a9f3161a5d411b0b3ce1d2"
  },
  {
    "upstream_path": "vendor/lib/chatgpt_client.py",
    "sha256": "71b90c6e4339244dd1e4c1c0ddd4a237dcb45eeec6b079fae4760a53a80fc15e"
  },
  {
    "upstream_path": "vendor/lib/http_client.py",
    "sha256": "f7ca5c4b12129dfc26f930f28e6f1aa9de2296c0997c0867118a69197784edcc"
  },
  {
    "upstream_path": "vendor/lib/utils.py",
    "sha256": "b848e288d0e1c71d2d0bd23c49d0378ad1879d03ea586a780914e8aae70a0f2c"
  },
  {
    "upstream_path": "vendor/lib/sentinel_token.py",
    "sha256": "6760d012b6d1146f13885df2d84d7c5219a4f799050a3c0a91ddc057622acb0c"
  },
  {
    "upstream_path": "app/totp.py",
    "sha256": "d81fefe6481ba4d8224837d96ffc9c7871e190502b108cb8d22f158366c49fcb"
  }
]
```
