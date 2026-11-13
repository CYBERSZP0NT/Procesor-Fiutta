# Maintainer: serainox420 <serainox@gmail.com>
pkgname=szmelc-router-git
pkgver=r0
pkgrel=1
pkgdesc="One static Go binary in front of all your free LLM capacity: OpenAI-compatible router with an agentic task router, TUI and FreeLLMAPI live routing"
arch=('x86_64' 'aarch64' 'armv7h')
url="https://github.com/CYBERSZP0NT/szmelc-router"
license=('MIT')
depends=('glibc')
makedepends=('git' 'go>=2:1.27.1')
optdepends=('ollama: local models'
            'docker: zero-key sidecar stack')
provides=('szmelc-router' 'freellm-router')
conflicts=('szmelc-router' 'freellm-router')
source=("${pkgname}::git+${url}.git")
sha256sums=('SKIP')

pkgver() {
  cd "${pkgname}"
  ( git describe --long --tags --abbrev=7 2>/dev/null | sed 's/^v//;s/\([^-]*-g\)/r\1/;s/-/./g' ) ||
    printf "r%s.%s" "$(git rev-list --count HEAD)" "$(git rev-parse --short=7 HEAD)"
}

prepare() {
  cd "${pkgname}"
  export GOPATH="${srcdir}/gopath"
  go mod download -x
}

build() {
  cd "${pkgname}"
  export GOPATH="${srcdir}/gopath"
  export CGO_ENABLED=0
  export GOFLAGS="-buildmode=pie -trimpath -mod=readonly -modcacherw"
  local commit date
  commit="$(git rev-parse --short HEAD)"
  date="$(date -u -d "@${SOURCE_DATE_EPOCH:-$(date +%s)}" +%Y-%m-%dT%H:%M:%SZ)"
  go build -o szmelc-router \
    -ldflags "-s -w -X 'main.version=${pkgver}' -X 'main.commit=${commit}' -X 'main.date=${date}'" \
    ./cmd/szmelc-router
  mkdir -p completions
  ./szmelc-router completion bash > completions/szmelc-router.bash
  ./szmelc-router completion zsh  > completions/_szmelc-router
  ./szmelc-router completion fish > completions/szmelc-router.fish
}

check() {
  cd "${pkgname}"
  export GOPATH="${srcdir}/gopath"
  go test ./...
}

package() {
  cd "${pkgname}"
  install -Dm755 szmelc-router "${pkgdir}/usr/bin/szmelc-router"
  # Legacy name from before the rebrand.
  ln -s szmelc-router "${pkgdir}/usr/bin/freellm-router"

  install -Dm644 completions/szmelc-router.bash "${pkgdir}/usr/share/bash-completion/completions/szmelc-router"
  install -Dm644 completions/_szmelc-router     "${pkgdir}/usr/share/zsh/site-functions/_szmelc-router"
  install -Dm644 completions/szmelc-router.fish "${pkgdir}/usr/share/fish/vendor_completions.d/szmelc-router.fish"

  install -Dm644 LICENSE "${pkgdir}/usr/share/licenses/${pkgname}/LICENSE"
  install -Dm644 README.md CHANGELOG.md MODELS.md FREE-MODELS.md .env.example -t "${pkgdir}/usr/share/doc/${pkgname}/"
  install -Dm644 docs/*.md -t "${pkgdir}/usr/share/doc/${pkgname}/docs/"
}
