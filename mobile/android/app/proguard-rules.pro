# The WebView calls these by name.
-keepclassmembers class io.github.jeremiahm37.lectern.Bridge {
    @android.webkit.JavascriptInterface <methods>;
}
-keepattributes JavascriptInterface
