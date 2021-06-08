module.exports = async function(ws, clients) {
   
    ws.on('message', function incoming(data) {
        clients.forEach(function each(client) {
              client.send(data);
          });
        });

    ws.on('close', function close() {
        return {
            status: 200,
            message: "I am done"
        }
    });
}
