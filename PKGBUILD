# Maintainer: Caleb Eastman <eastmancr@gmail.com>
pkgname=webmux
pkgver=0.1
pkgrel=1
pkgdesc="Browser-based terminal multiplexer with tmux persistence"
arch=('x86_64' 'aarch64')
url="https://github.com/eastmancr/webmux"
license=('GPL-3.0-or-later')
depends=('ttyd' 'tmux')
makedepends=('go')
options=('!strip' '!debug') # Go binaries don't play well with standard strip/debug

source=("$pkgname-$pkgver.tar.gz")
sha256sums=('SKIP')

build() {
  cd "$srcdir/$pkgname-$pkgver" || return 1
  export CGO_ENABLED=0

  # Build wm CLI first (gets embedded in webmux)
  go build -trimpath -buildmode=pie -ldflags "-s -w" -o static/wm ./cmd/wm

  # Build webmux with embedded static files
  go build -trimpath -buildmode=pie -ldflags "-s -w" -o webmux .
}

package() {
  cd "$srcdir/$pkgname-$pkgver" || return 1

  install -Dm755 webmux "$pkgdir/usr/bin/webmux"
  install -Dm644 webmux.1 "$pkgdir/usr/share/man/man1/webmux.1"
  install -Dm644 README.md "$pkgdir/usr/share/doc/$pkgname/README.md"
  install -Dm644 LICENSE "$pkgdir/usr/share/licenses/$pkgname/LICENSE"
}
