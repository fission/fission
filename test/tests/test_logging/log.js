module.exports = async function (context) {
  console.log("log test");
  return {
    status: 200,
    body: "log test\n",
  };
};
