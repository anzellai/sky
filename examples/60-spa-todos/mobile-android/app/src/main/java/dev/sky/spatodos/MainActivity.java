package dev.sky.spatodos;

import android.app.Activity;
import android.os.Bundle;
import android.webkit.WebSettings;
import android.webkit.WebView;
import android.webkit.WebViewClient;

/**
 * The native mobile shell for the Sky.Spa Todos client.
 *
 * A thin WebView that loads the SAME client the web + desktop builds use: the
 * Sky TEA loop (Model / Msg / update / view) compiled to wasm, served over HTTP
 * by its own stateless backend. Client and server stay SEPARATE; only the shell
 * is native — identical in concept to Std.Webview.url on desktop.
 *
 * 10.0.2.2 is the Android emulator's alias for the host's localhost, so this
 * loads the backend started on the host at TODOS_PORT (default 8951). For a real
 * device / production, point APP_URL at the deployed backend over https.
 */
public class MainActivity extends Activity {

    private static final String APP_URL = "http://10.0.2.2:8951/";

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        WebView web = new WebView(this);
        WebSettings s = web.getSettings();
        s.setJavaScriptEnabled(true);   // the wasm bootstrap needs JS
        s.setDomStorageEnabled(true);
        web.setWebViewClient(new WebViewClient());  // keep navigation inside the WebView
        setContentView(web);
        web.loadUrl(APP_URL);
    }
}
