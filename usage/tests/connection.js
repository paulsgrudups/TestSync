const W3CWebSocket = require("websocket").w3cwebsocket;
const WebSocketAsPromised = require("websocket-as-promised");

// IMPORTANT: The following code is an example use case for NightWatch test 
// automation framework. Incorrect use of async and not closing the WebSocket 
// connection fully can result in corrupted tests because of the process not 
// stopping.

module.exports = {
  test: async (client) => {
    // TestSync requires credentials unless the server was started with
    // authentication explicitly disabled. They are sent in the Authorization
    // header; the ?username=&password= query fallback is deprecated because
    // query strings end up in proxy and access logs.
    const user = process.env.TESTSYNC_USER || "";
    const pass = process.env.TESTSYNC_PASS || "";
    const headers = user || pass
      ? {
          Authorization:
            "Basic " + Buffer.from(user + ":" + pass).toString("base64"),
        }
      : null;

    // Initialize the WebSocket
    const wsp = new WebSocketAsPromised("ws://localhost:9105/register/1", {
      createWebSocket: (url) => new W3CWebSocket(url, null, null, headers),
      packMessage: (data) => JSON.stringify(data),
      unpackMessage: (data) => JSON.parse(data),
    });

    // Async sleep function
    const sleep = (ms) => {
      return new Promise((resolve) => setTimeout(resolve, ms));
    };

    // Open the WebSocket connection
    const connectionEstablished = async () => {
      try {
        console.log("[TestSync] Opening WS connection");
        await wsp.open();
        console.log("[TestSync] WS connection established");
      } catch (err) {
        console.error("WS Error", err);
      }
    };

    // Close the WebSocket connection
    // TODO: Find a way to close the connection without special message command
    const closeConnection = async () => {
      if (wsp.isClosed) {
        console.log("[TestSync] WS is already closed");
        return;
      }

      console.log("[TestSync] Sending close WS message");
      wsp.sendPacked({ command: "close" });

      console.log("[TestSync] Waiting to close WS connection");
      await wsp.close();
      console.log("[TestSync] WS connection closed");
    };

    // Send a message to the WebSocket connection
    const sendMessage = (message) => {
      if (!wsp.isOpened) {
        throw new Error("Send message failed, connection closed");
      }

      wsp.sendPacked(message);
    };

    // Recieve a message from the WebSocket connection
    // TODO: Most likely can remove check if message is ping since it's handling
    // is built-in
    const receiveMessage = async () => {
      if (!wsp.isOpened) {
        throw new Error("Receive message failed, connection closed");
      }

      const data = await wsp.waitUnpackedMessage((data) => data);

      if (data === "ping") {
        return await receiveMessage();
      }

      return data;
    };

    // Start of the use of functions as a PoC how they can be combined

    console.log("[TestSync] Trying to establish WS connection");
    await connectionEstablished();

    console.log("[TestSync] Sending WS message");

    sendMessage({
      command: "wait_checkpoint",
      content: { target_count: 1, identifier: "test" },
    });

    console.log("[TestSync] Waiting to receive WS message");

    const message = await receiveMessage();

    console.log("[TestSync] Response data:", message);
    console.log(
      "[TestSync] Should continue in:",
      message.content.start_at - Date.now(),
      "miliseconds"
    );

    await sleep(message.content.start_at - Date.now())

    // At this point the participants should be synchronized here.

    await closeConnection();
  },
};
