#!/bin/bash
set -e

# Configuration - Update these for your organization
BITBUCKET_ORG="yourorg"
TAP_REPO="homebrew-tunnelboy"

VERSION=${BITBUCKET_TAG#v}  # Remove 'v' prefix
REPO_URL="https://bitbucket.org/${BITBUCKET_ORG}/tunnelboy/downloads"

echo "Updating Homebrew formula for version ${VERSION}..."

# Calculate SHA256 for each archive
SHA_AMD64=$(sha256sum tunnelboy_darwin_amd64.tar.gz | awk '{print $1}')
SHA_ARM64=$(sha256sum tunnelboy_darwin_arm64.tar.gz | awk '{print $1}')

echo "SHA256 (amd64): ${SHA_AMD64}"
echo "SHA256 (arm64): ${SHA_ARM64}"

# Clone homebrew tap repo
echo "Cloning tap repository..."
git clone "https://x-token-auth:${BITBUCKET_APP_PASSWORD}@bitbucket.org/${BITBUCKET_ORG}/${TAP_REPO}.git"
cd "${TAP_REPO}"

# Create Formula directory if it doesn't exist
mkdir -p Formula

# Generate formula
echo "Generating formula..."
cat > Formula/tunnelboy.rb << EOF
class Tunnelboy < Formula
  desc "AWS VPC tunneling CLI with Pip-Boy theming"
  homepage "https://bitbucket.org/${BITBUCKET_ORG}/tunnelboy"
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
      TunnelBoy requires the AWS Session Manager plugin.
      Install it with:
        brew install --cask session-manager-plugin

      For usage information:
        tunnelboy --help
    EOS
  end

  test do
    system "#{bin}/tunnelboy", "version"
  end
end
EOF

# Commit and push
echo "Committing changes..."
git config user.email "pipelines@${BITBUCKET_ORG}.com"
git config user.name "Bitbucket Pipelines"
git add Formula/tunnelboy.rb
git commit -m "Update tunnelboy to ${VERSION}"

echo "Pushing to remote..."
git push origin main

echo "Homebrew formula updated successfully!"
