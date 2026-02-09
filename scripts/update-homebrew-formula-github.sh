#!/bin/bash
set -e

# Configuration
GITHUB_ORG="adamw2"
MAIN_REPO="tunnelboy"
TAP_REPO="homebrew-tunnelboy"

VERSION="${GITHUB_REF#refs/tags/v}"
REPO_URL="https://github.com/${GITHUB_ORG}/${MAIN_REPO}/releases/download/v${VERSION}"

echo "Updating Homebrew formula for version ${VERSION}..."

# Download the release assets to calculate SHA256
curl -L "${REPO_URL}/tunnelboy_darwin_amd64.tar.gz" -o tunnelboy_darwin_amd64.tar.gz
curl -L "${REPO_URL}/tunnelboy_darwin_arm64.tar.gz" -o tunnelboy_darwin_arm64.tar.gz

# Calculate SHA256 for each archive
SHA_AMD64=$(shasum -a 256 tunnelboy_darwin_amd64.tar.gz | awk '{print $1}')
SHA_ARM64=$(shasum -a 256 tunnelboy_darwin_arm64.tar.gz | awk '{print $1}')

echo "SHA256 (amd64): ${SHA_AMD64}"
echo "SHA256 (arm64): ${SHA_ARM64}"

# Clone homebrew tap repo
echo "Cloning tap repository..."
git clone "https://x-token-auth:${GITHUB_TOKEN}@github.com/${GITHUB_ORG}/${TAP_REPO}.git"
cd "${TAP_REPO}"

# Create Formula directory if it doesn't exist
mkdir -p Formula

# Generate formula
echo "Generating formula..."
cat > Formula/tunnelboy.rb << EOF
class Tunnelboy < Formula
  desc "AWS VPC tunneling CLI with Pip-Boy theming"
  homepage "https://github.com/${GITHUB_ORG}/${MAIN_REPO}"
  version "${VERSION}"
  license "MIT"

  on_macos do
    if Hardware::CPU.arm?
      url "${REPO_URL}/tunnelboy_darwin_arm64.tar.gz"
      sha256 "${SHA_ARM64}"
    else
      url "${REPO_URL}/tunnelboy_darwin_amd64.tar.gz"
      sha256 "${SHA_AMD64}"
    end
  end

  depends_on :macos

  def install
    bin.install "tunnelboy"
  end

  def caveats
    <<~EOS
      TunnelBoy requires the AWS Session Manager plugin:
        brew install --cask session-manager-plugin

      To enable shell completion (copy and paste):
        grep -qxF 'autoload -Uz compinit && compinit' ~/.zshrc || echo 'autoload -Uz compinit && compinit' >> ~/.zshrc
        mkdir -p ~/.zsh/completions && tunnelboy completion zsh > ~/.zsh/completions/_tunnelboy
        grep -qxF 'fpath=(~/.zsh/completions \$fpath)' ~/.zshrc || echo 'fpath=(~/.zsh/completions \$fpath)' >> ~/.zshrc
        source ~/.zshrc

      Get started:
        tunnelboy profile list
        tunnelboy connect rds
    EOS
  end

  test do
    system "#{bin}/tunnelboy", "version"
  end
end
EOF

# Commit and push
echo "Committing changes..."
git config user.email "actions@github.com"
git config user.name "GitHub Actions"
git add Formula/tunnelboy.rb
git commit -m "Update tunnelboy to ${VERSION}"

echo "Pushing to remote..."
git push origin main

echo "Homebrew formula updated successfully!"
