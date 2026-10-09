# Android release signing key

Release APKs (built by the `android` job for a version tag or a "Run workflow"
release) are signed with a permanent key so that each release installs over the
previous one. Android only accepts an update signed with the same key, so
**keep a backup of the keystore and its password**: if it is lost, users must
uninstall the app to install a build signed with a new key.

The key lives only in GitHub secrets (and your backup), never in the
repository. Without the secrets a release fails rather than shipping an
unsigned or debug-signed APK; ordinary builds still produce a debug APK.

## One-time setup

1. Create the keystore on your own machine (needs a JDK for `keytool`). Pick a
   strong password; use the same one for the store and the key when asked.

   ```sh
   keytool -genkeypair -keystore franklinwh-release.jks -storetype PKCS12 \
     -alias franklinwh -keyalg RSA -keysize 4096 -validity 10000 \
     -dname "CN=franklinwh"
   ```

2. Add four repository secrets under **Settings → Secrets and variables →
   Actions → New repository secret** (or with the `gh` commands below):

   | Secret | Value |
   |---|---|
   | `ANDROID_KEYSTORE_BASE64` | the keystore, base64-encoded |
   | `ANDROID_KEYSTORE_PASSWORD` | the keystore password |
   | `ANDROID_KEY_ALIAS` | `franklinwh` (the `-alias` above) |
   | `ANDROID_KEY_PASSWORD` | the key password (same as the store's for PKCS12) |

   ```sh
   base64 -w0 franklinwh-release.jks | gh secret set ANDROID_KEYSTORE_BASE64 -R lanrat/Franklinwh
   gh secret set ANDROID_KEYSTORE_PASSWORD -R lanrat/Franklinwh   # prompts for the value
   gh secret set ANDROID_KEY_ALIAS -R lanrat/Franklinwh -b franklinwh
   gh secret set ANDROID_KEY_PASSWORD -R lanrat/Franklinwh        # prompts for the value
   ```

   (On macOS use `base64 -i franklinwh-release.jks` instead of `base64 -w0`.)

3. Back up `franklinwh-release.jks` and the password somewhere safe (a password
   manager), then delete the local copy if you like.

## Building a signed APK locally

```sh
scripts/build-aar.sh
cd android
ANDROID_KEYSTORE_FILE=/path/to/franklinwh-release.jks \
ANDROID_KEYSTORE_PASSWORD=... ANDROID_KEY_ALIAS=franklinwh ANDROID_KEY_PASSWORD=... \
  ./gradlew :app:assembleRelease -PappVersionName=1.2.3
# -> app/build/outputs/apk/release/app-release.apk
```

## First install

Earlier releases (v0.2.1 and before) were signed with throwaway debug keys.
Uninstall that version once before installing the first release signed with
the permanent key; after that, releases update in place.
