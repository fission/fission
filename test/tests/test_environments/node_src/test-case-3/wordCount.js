module.exports = async function (context) {
  var splitStringArray = context.request.split(" ");

  return {
    status: 200,
    body: splitStringArray.length,
  };
};
