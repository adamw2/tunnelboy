# GitHub Release Setup Guide

This guide will help you complete the GitHub Actions setup for automatic Homebrew releases.

## Files Created

✅ `.github/workflows/release.yml` - GitHub Actions workflow
✅ `scripts/update-homebrew-formula-github.sh` - Formula update script
✅ Updated README.md with correct installation instructions

## Prerequisites Completed

✅ GitHub repository created: `github.com/adamw2/tunnelboy`
✅ Homebrew tap repository created: `github.com/adamw2/homebrew-tunnelboy`
✅ Code pushed to GitHub

## Final Setup Steps

### 1. Create GitHub Personal Access Token

You need to create a token that allows the workflow to update your tap repository.

1. Go to: https://github.com/settings/tokens?type=beta
2. Click **"Generate new token"** → **"Fine-grained tokens"**
3. Configure the token:
   - **Token name**: `TunnelBoy Homebrew Tap Updater`
   - **Expiration**: 1 year (or longer)
   - **Repository access**: Only select repositories
     - Select: `adamw2/homebrew-tunnelboy`
   - **Permissions**:
     - Repository permissions → Contents: **Read and write**
4. Click **"Generate token"**
5. **Copy the token** (you won't see it again!)

### 2. Add Token to Repository Secrets

1. Go to your main TunnelBoy repository:
   - https://github.com/adamw2/tunnelboy/settings/secrets/actions
2. Click **"New repository secret"**
3. Add:
   - **Name**: `TAP_GITHUB_TOKEN`
   - **Value**: Paste the token you just created
4. Click **"Add secret"**

### 3. Commit and Push the New Files

```bash
cd ~/repos/tunnelboy

# Add the new files
git add .github/workflows/release.yml
git add scripts/update-homebrew-formula-github.sh
git add README.md
git add GITHUB_RELEASE_SETUP.md

# Commit
git commit -m "Add GitHub Actions workflow for Homebrew releases"

# Push to GitHub
git push origin main
```

## Testing the Release

Once everything is set up, test it by creating a release:

```bash
cd ~/repos/tunnelboy

# Create and push a version tag
git tag v0.1.0
git push origin v0.1.0
```

### What Will Happen

When you push the tag, GitHub Actions will automatically:

1. ✅ Build macOS binaries (Intel + Apple Silicon)
2. ✅ Create a GitHub Release with binaries
3. ✅ Download binaries and calculate SHA256
4. ✅ Clone your homebrew-tunnelboy repo
5. ✅ Generate `Formula/tunnelboy.rb`
6. ✅ Commit and push to homebrew-tunnelboy

### Monitor Progress

Watch the workflow run:
- Go to: https://github.com/adamw2/tunnelboy/actions
- Click on the workflow run
- Watch each step complete

### Verify Success

After the workflow completes:

1. Check the release: https://github.com/adamw2/tunnelboy/releases
2. Check the formula: https://github.com/adamw2/homebrew-tunnelboy/blob/main/Formula/tunnelboy.rb
3. Test installation:
   ```bash
   brew tap adamw2/tunnelboy
   brew install tunnelboy
   tunnelboy version
   ```

## Troubleshooting

### Workflow Fails at "Update Homebrew formula"

**Error**: Permission denied or authentication failed

**Solution**: Make sure the `TAP_GITHUB_TOKEN` secret has write access to the homebrew-tunnelboy repository.

### Formula Not Generated

**Error**: Script runs but no formula appears

**Solution**: Check the Actions logs for errors. Common issues:
- Token doesn't have write permissions
- Tap repository doesn't exist
- Wrong repository name in script

### Download URLs Don't Work

**Error**: `curl` fails to download binaries

**Solution**: The workflow needs a few minutes after creating the release for the binaries to be available. This is usually not an issue, but if it persists, add a `sleep 30` before the download commands.

## Future Releases

After the initial setup, releasing new versions is easy:

```bash
# Make your changes
git add .
git commit -m "Your changes"
git push origin main

# Create and push a new version tag
git tag v0.2.0
git push origin v0.2.0
```

That's it! The workflow handles everything automatically.

## Keeping Bitbucket (Optional)

If you want to keep Bitbucket as a mirror:

```bash
# Keep both remotes
git remote -v
# Should show:
#   origin    git@github.com:adamw2/tunnelboy.git
#   bitbucket git@bitbucket.org:yourorg/tunnelboy.git (if you added it)

# Push to both
git push origin main
git push bitbucket main

# Note: Only GitHub tags will trigger releases
```

## Summary

- ✅ Workflow and scripts created
- ⏳ **TODO**: Create GitHub Personal Access Token
- ⏳ **TODO**: Add token as `TAP_GITHUB_TOKEN` secret
- ⏳ **TODO**: Commit and push changes
- ⏳ **TODO**: Test with `git tag v0.1.0 && git push origin v0.1.0`

Once these steps are complete, you'll have a fully automated Homebrew release system!
