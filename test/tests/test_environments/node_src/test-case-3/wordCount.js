module.exports = async function (context) {
  var splitStringArray = context.request.body.split(" ");

  return {
    status: 200,
    body: "word count " + splitStringArray.length,
  };
};
